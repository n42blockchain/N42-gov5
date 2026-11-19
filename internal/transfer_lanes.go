// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Transfer lanes (r98a; design r97 section R1, owner decision OD-1): a
// strict Block-STM run of a block made solely of plain value transfers is
// applied without the multi-version store's per-transaction reads, writes
// and validation. A lane is one sender's transactions in block order, run
// by the worker the first wave's partition gives that sender, against a
// private copy of the sender's account; the sender's final account is
// published once. A recipient that sends nothing in the block (a pure
// recipient) is only ever credited: its credits are summed per recipient
// and published as one balance delta.
//
// The store's final versions -- the only thing its consumers read (the fold
// into the IntraBlockState through ApplyShards, the early snapshot at
// numTxs) -- and every transaction's result slot (receipt, gas, logs, fees)
// are what the plan path (tryDirectTransferPlan) leaves for the same block.
// Any condition the lanes do not cover declines the block before anything is
// published, and the run takes today's path untouched: the lanes never
// produce an error, so an invalid transaction fails the block exactly as
// today.
//
// N42_EXEC_TRANSFER_LANES: unset or "0" off (default); "1" (or "true", "on")
// runs the lanes; "check" (or "2") runs the lanes into a scratch store and
// today's path into the real one, compares the two, logs an Error on any
// difference (a panic in test binaries) and uses today's result.

package internal

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// EnvExecTransferLanes names the transfer lanes' switch.
const EnvExecTransferLanes = "N42_EXEC_TRANSFER_LANES"

const (
	lanesOff = iota
	lanesOn
	lanesCheck
)

// transferLanesMode is read once at init (SetTransferLanesForTest in tests).
var transferLanesMode = parseTransferLanes(os.Getenv(EnvExecTransferLanes))

func parseTransferLanes(v string) int {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return lanesOn
	}
	return lanesOff
}

// SetTransferLanesForTest sets N42_EXEC_TRANSFER_LANES ("off", "on" or
// "check") and returns the restore.
func SetTransferLanesForTest(mode string) func() {
	prev := transferLanesMode
	switch mode {
	case "on", "1":
		transferLanesMode = lanesOn
	default:
		transferLanesMode = lanesOff
	}
	return func() { transferLanesMode = prev }
}

// transferLanesStats: blocks the lanes applied, blocks they declined, check
// mode comparisons and check mode differences (must stay 0).
var transferLanesStats struct {
	used, declined, checked, mismatches atomic.Uint64
}

// TransferLanesStats returns the lanes' counters since start.
func TransferLanesStats() (used, declined, checked, mismatches uint64) {
	return transferLanesStats.used.Load(), transferLanesStats.declined.Load(),
		transferLanesStats.checked.Load(), transferLanesStats.mismatches.Load()
}

// laneInputs is what the lanes read of a strict Block-STM run.
type laneInputs struct {
	numTxs int
	// queues is the first wave's partition (Executor.FirstWaveQueues): every
	// transaction once, each queue in index order, one sender's transactions
	// on one queue.
	queues   [][]int
	prepared *preparedSet
	header   *block.Header
	// setup opens a worker's context exactly as the run's workers do (its
	// own read transaction and base reader over the block's base cache);
	// env returns that context's EVM and reader.
	setup parallel.WorkerSetupFunc
	env   func(ctx any) (*vm2.EVM, *parallel.ParallelStateReader, bool)
	// noReceipts mirrors the run's vm.Config.NoReceipts (declined when set).
	noReceipts bool
}

// laneOutcome is a lane run's result and its "parallel block" stamps.
type laneOutcome struct {
	used bool
	// why the block was declined ("" when used).
	why string
	// lanes: distinct senders; top: the longest lane; recipients: distinct
	// pure recipients.
	lanes, top, recipients int
	classify, merge, run, publish time.Duration
	// check: the check mode's verdict ("same", "diff"; "" when not compared).
	check string
}

// laneAcc is one lane: a sender, its private account and the last
// transaction that wrote it.
type laneAcc struct {
	addr   types.Address
	acc    account.StateAccount
	last   int
	n      int
	loaded bool
}

// laneCredit is one transaction's credit to its recipient.
type laneCredit struct {
	addr  types.Address
	value uint64
	tx    int32
}

// laneRecipient is a pure recipient's summed credits and the last
// transaction that credited it.
type laneRecipient struct {
	addr types.Address
	sum  uint256.Int
	last int
}

// laneWorker is one worker's state across the phases.
type laneWorker struct {
	lanes  []laneAcc
	laneOf []int32 // lane of each queue position
	// senders and credits by shard (laneShard).
	senders  [][]types.Address
	credits  [][]laneCredit
	receipts []block.Receipt
}

// laneRun is one block's lane run.
type laneRun struct {
	in      *laneInputs
	mvs     *parallel.MVS
	res     []parallelTxResult
	workers int
	wk      []laneWorker
	recips  [][]laneRecipient // by shard
	// declined is set once; why is the first reason.
	declined atomic.Bool
	why      atomic.Pointer[string]
	// barrier[k] ends phase k (classify, merge, run) for every worker.
	barrier [3]sync.WaitGroup
	// phase ends, stamped by worker 0 as it leaves each barrier.
	tPhase [3]time.Time
}

// decline records why the block cannot take the lanes (the first reason
// wins) and stops every worker at its next check.
func (r *laneRun) decline(format string, args ...any) {
	if r.declined.CompareAndSwap(false, true) {
		s := fmt.Sprintf(format, args...)
		r.why.Store(&s)
	}
}

// laneShard maps an address to one of n shards (the merge and publish
// split). Every occurrence of an address lands in the same shard, so a
// sender and a recipient that are one account meet in one shard.
func laneShard(a *types.Address, n int) int {
	h := binary.LittleEndian.Uint64(a[0:8])*0x9E3779B97F4A7C15 ^ binary.LittleEndian.Uint64(a[8:16])*0xC2B2AE3D27D4EB4F ^
		uint64(binary.LittleEndian.Uint32(a[16:20]))*0x165667B19E3779F9
	h ^= h >> 29
	return int(h % uint64(n))
}

// laneBlockDeclines is the lanes' block-level qualification: the facts
// that need no transaction. "" when the block may take the lanes.
func laneBlockDeclines(in *laneInputs) string {
	switch {
	case !(transferFast2 && transferFastPath && transferDirectPath):
		return "plan path off"
	case in.noReceipts:
		return "no receipts"
	case in.header == nil || in.header.BaseFee == nil:
		return "no base fee"
	case params.TxGas > in.header.GasLimit:
		return "gas limit below a transfer"
	case in.prepared == nil || len(in.prepared.txs) < in.numTxs:
		return "no preparation"
	case len(in.queues) == 0:
		return "no partition"
	}
	n := 0
	for _, q := range in.queues {
		n += len(q)
	}
	if n != in.numTxs {
		return "partition incomplete"
	}
	return ""
}

// classify is phase 1 on worker w's queue: every transaction must be a plan
// transfer of a covered type whose fee cap the block admits, and each
// distinct sender becomes a lane; credits are bucketed by recipient shard.
func (r *laneRun) classify(w int, bp *planBlock) {
	q := r.in.queues[w]
	wk := &r.wk[w]
	nShards := r.workers
	wk.laneOf = make([]int32, len(q))
	wk.credits = make([][]laneCredit, nShards)
	per := len(q)/nShards + len(q)/(4*nShards) + 4
	for s := range wk.credits {
		wk.credits[s] = make([]laneCredit, 0, per)
	}
	idx := make(map[types.Address]int32, len(q)/8+8)
	var feeCap uint256.Int
	prev := -1
	for k, i := range q {
		if i <= prev {
			r.decline("queue %d not in index order at %d", w, i)
			return
		}
		prev = i
		pm := &r.in.prepared.txs[i]
		if !pm.ok || !pm.plan.ok {
			r.decline("tx %d is not a plan transfer", i)
			return
		}
		pl := &pm.plan
		if pl.txType != transaction.LegacyTxType && pl.txType != transaction.DynamicFeeTxType {
			r.decline("tx %d has type %d", i, pl.txType)
			return
		}
		// CheckEip1559TxGasFeeCap as tryDirectTransferPlan runs it.
		if !bp.noBaseFee || pl.feeCap != 0 || pl.tip != 0 {
			feeCap.SetUint64(pl.feeCap)
			if pl.feeCap < pl.tip || feeCap.Lt(bp.baseFee) {
				r.decline("tx %d fee cap", i)
				return
			}
		}
		li, ok := idx[pl.from]
		if !ok {
			li = int32(len(wk.lanes))
			idx[pl.from] = li
			wk.lanes = append(wk.lanes, laneAcc{addr: pl.from, last: -1})
		}
		wk.lanes[li].n++
		wk.laneOf[k] = li
		s := laneShard(&pl.to, nShards)
		wk.credits[s] = append(wk.credits[s], laneCredit{addr: pl.to, value: pl.value, tx: int32(i)})
	}
	wk.senders = make([][]types.Address, nShards)
	for li := range wk.lanes {
		a := &wk.lanes[li].addr
		s := laneShard(a, nShards)
		wk.senders[s] = append(wk.senders[s], *a)
	}
}

// merge is phase 2 on shard p (= worker): a sender must run on one worker,
// no recipient may send or receive fees, and every pure recipient must
// exist in the base, carry no code, take its credits as balance deltas (the
// state writer's rule) without overflow, and not be a precompile.
func (r *laneRun) merge(p int, evm *vm2.EVM, rd *parallel.ParallelStateReader, bp *planBlock) {
	senders := make(map[types.Address]int, 256)
	for v := range r.wk {
		for _, a := range r.wk[v].senders[p] {
			if _, dup := senders[a]; dup {
				r.decline("sender %x on two workers", a)
				return
			}
			if r.isFeeRecipient(a, bp) {
				r.decline("sender %x receives fees", a)
				return
			}
			senders[a] = v
		}
	}
	n := 0
	for v := range r.wk {
		n += len(r.wk[v].credits[p])
	}
	at := make(map[types.Address]int32, n/4+8)
	recips := make([]laneRecipient, 0, n/4+8)
	for v := range r.wk {
		for _, c := range r.wk[v].credits[p] {
			j, ok := at[c.addr]
			if !ok {
				if _, sends := senders[c.addr]; sends {
					r.decline("tx %d: recipient %x also sends", c.tx, c.addr)
					return
				}
				if r.isFeeRecipient(c.addr, bp) {
					r.decline("tx %d: recipient %x receives fees", c.tx, c.addr)
					return
				}
				j = int32(len(recips))
				at[c.addr] = j
				recips = append(recips, laneRecipient{addr: c.addr, last: -1})
			}
			rc := &recips[j]
			rc.sum.AddUint64(&rc.sum, c.value)
			if int(c.tx) > rc.last {
				rc.last = int(c.tx)
			}
		}
	}
	var to account.StateAccount
	var total uint256.Int
	for j := range recips {
		if j&255 == 0 && r.declined.Load() {
			return
		}
		rc := &recips[j]
		base, err := rd.ReadBaseAccount(rc.addr)
		if err != nil {
			r.decline("recipient %x: %v", rc.addr, err)
			return
		}
		if base == nil {
			r.decline("recipient %x absent", rc.addr)
			return
		}
		to = *base
		normaliseLoaded(&to)
		if to.CodeHash != emptyCodeHash {
			r.decline("recipient %x has code", rc.addr)
			return
		}
		// UpdateAccountDataEligible's delta rule for original = the base
		// account, acc = its normalised copy plus the value: every field but
		// the balance unchanged, the balance larger.
		if base.Nonce != to.Nonce || base.CodeHash != to.CodeHash || base.Root != to.Root || base.Initialised != to.Initialised {
			r.decline("recipient %x takes no balance delta", rc.addr)
			return
		}
		if _, overflow := total.AddOverflow(&base.Balance, &rc.sum); overflow {
			r.decline("recipient %x balance overflow", rc.addr)
			return
		}
		if evm.IsPrecompile(rc.addr) {
			r.decline("recipient %x is a precompile", rc.addr)
			return
		}
	}
	r.recips[p] = recips
}

// isFeeRecipient reports whether a receives the block's priority fee or
// burnt base fee.
func (r *laneRun) isFeeRecipient(a types.Address, bp *planBlock) bool {
	return a == bp.tipTo || (bp.burn && bp.burnTo != nil && a == *bp.burnTo)
}
