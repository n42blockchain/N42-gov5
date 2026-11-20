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
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	cmath "github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state/commitment"
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
	case "2", "check":
		return lanesCheck
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
	case "check":
		transferLanesMode = lanesCheck
	default:
		transferLanesMode = lanesOff
	}
	return func() { transferLanesMode = prev }
}

// transferLanesModeName is the switch for the "parallel block" line.
func transferLanesModeName() string {
	switch transferLanesMode {
	case lanesOn:
		return "on"
	case lanesCheck:
		return "check"
	}
	return "off"
}

// transferLanesStats: blocks the lanes applied, blocks they declined, check
// mode comparisons and check mode differences (must stay 0).
var transferLanesStats struct {
	used, declined, checked, mismatches atomic.Uint64
}

// rootHintsHanded counts the root ops hints lane blocks handed their state
// (N42_ROOT_OPS_FROM_RUN / N42_ROOT_OPS_UNLOCKED; parallel_processor.go).
var rootHintsHanded atomic.Uint64

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
	lanes, top, recipients        int
	classify, merge, run, publish time.Duration
	// check: the check mode's verdict ("same", "diff"; "" when not compared).
	check string
	// applied is the applied run (used only), whose final accounts hand the
	// root its ops hint (rootFinals).
	applied *laneRun
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
	// final is the recipient's account after the block: the base with the
	// credits added (the root ops hint's input, r100a).
	final account.StateAccount
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

// runTransferLanes applies a block of plain transfers as lanes into mvs and
// res (the run's store and result slots, or a check's scratch ones). On
// decline nothing has been written to mvs; res may hold lane results for
// some transactions, which today's path overwrites (every transaction it
// runs sets every field of its slot).
func runTransferLanes(in *laneInputs, mvs *parallel.MVS, res []parallelTxResult) (out laneOutcome) {
	t0 := time.Now()
	if why := laneBlockDeclines(in); why != "" {
		out.why = why
		out.classify = time.Since(t0)
		transferLanesStats.declined.Add(1)
		return out
	}
	r := &laneRun{in: in, mvs: mvs, res: res, workers: len(in.queues)}
	r.wk = make([]laneWorker, r.workers)
	r.recips = make([][]laneRecipient, r.workers)
	for k := range r.barrier {
		r.barrier[k].Add(r.workers)
	}
	var done sync.WaitGroup
	for w := 0; w < r.workers; w++ {
		done.Add(1)
		go func(w int) {
			defer done.Done()
			r.worker(w)
		}(w)
	}
	done.Wait()
	t4 := time.Now()
	if !r.tPhase[2].IsZero() {
		out.classify = r.tPhase[0].Sub(t0)
		out.merge = r.tPhase[1].Sub(r.tPhase[0])
		out.run = r.tPhase[2].Sub(r.tPhase[1])
		out.publish = t4.Sub(r.tPhase[2])
	} else {
		out.classify = t4.Sub(t0)
	}
	for w := range r.wk {
		out.lanes += len(r.wk[w].lanes)
		for i := range r.wk[w].lanes {
			out.top = max(out.top, r.wk[w].lanes[i].n)
		}
		out.recipients += len(r.recips[w])
	}
	if r.declined.Load() {
		if p := r.why.Load(); p != nil {
			out.why = *p
		}
		transferLanesStats.declined.Add(1)
		return out
	}
	out.used = true
	out.applied = r
	return out
}

// rootFinals are an applied run's final accounts, one per account it
// changed: each lane's sender as published, each pure recipient's base plus
// its credits. Senders and pure recipients are disjoint (merge declines a
// recipient that sends), so each address appears once.
func (r *laneRun) rootFinals() []commitment.HintAccount {
	n := 0
	for w := range r.wk {
		n += len(r.wk[w].lanes) + len(r.recips[w])
	}
	out := make([]commitment.HintAccount, 0, n)
	for w := range r.wk {
		for li := range r.wk[w].lanes {
			ln := &r.wk[w].lanes[li]
			out = append(out, commitment.HintAccount{Addr: ln.addr, Acct: ln.acc})
		}
		for j := range r.recips[w] {
			rc := &r.recips[w][j]
			out = append(out, commitment.HintAccount{Addr: rc.addr, Acct: rc.final})
		}
	}
	return out
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

// worker runs worker w's part of every phase. A phase is skipped once the
// block is declined; every worker passes every barrier, a panicking one
// included.
func (r *laneRun) worker(w int) {
	passed := 0
	defer func() {
		if p := recover(); p != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			r.decline("panic in lane worker %d: %v", w, p)
			log.Error("transfer lanes: worker panic, block declined", "worker", w, "panic", p, "stack", string(buf[:n]))
		}
		for ; passed < len(r.barrier); passed++ {
			r.barrier[passed].Done()
		}
	}()
	wait := func() {
		passed++
		r.barrier[passed-1].Done()
		r.barrier[passed-1].Wait()
		if w == 0 {
			r.tPhase[passed-1] = time.Now()
		}
	}
	ctx, teardown, err := r.in.setup(w)
	if teardown != nil {
		defer teardown()
	}
	var evm *vm2.EVM
	var rd *parallel.ParallelStateReader
	var bp planBlock
	if err != nil {
		r.decline("worker %d setup: %v", w, err)
	} else if e, reader, ok := r.in.env(ctx); !ok || e == nil || reader == nil {
		r.decline("worker %d has no state reader", w)
	} else {
		evm, rd = e, reader
		bp = newPlanBlock(evm)
		if !bp.ok || bp.baseFee == nil {
			r.decline("block admits no plan transfer")
		}
	}
	if !r.declined.Load() {
		r.classify(w, &bp)
	}
	wait()
	if !r.declined.Load() {
		r.merge(w, evm, rd, &bp)
	}
	wait()
	if !r.declined.Load() {
		r.runLanes(w, rd, &bp)
	}
	wait()
	if !r.declined.Load() {
		r.publish(w)
	}
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
		rc.final = to
		rc.final.Balance = total
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

// runLanes is phase 3 on worker w: its queue in index order, each
// transaction on its lane's private account, with tryDirectTransferPlan's
// checks, arithmetic, receipt and fee credits.
func (r *laneRun) runLanes(w int, rd *parallel.ParallelStateReader, bp *planBlock) {
	q := r.in.queues[w]
	wk := &r.wk[w]
	hdr := r.in.header
	wk.receipts = make([]block.Receipt, len(q))
	const gas = params.TxGas
	var gasU, feeCap, tip, value, mgval, balanceCheck, amount, burn, tipRoom uint256.Int
	gasU.SetUint64(gas)
	var fees [2]deferredFee
	for k, i := range q {
		if k&255 == 0 && r.declined.Load() {
			return
		}
		ln := &wk.lanes[wk.laneOf[k]]
		pm := &r.in.prepared.txs[i]
		pl := &pm.plan
		if !ln.loaded {
			// The lane's first transaction reads the sender as a
			// transaction's read does while nothing in the block has
			// written it: the base account, through the block's cache.
			base, err := rd.ReadBaseAccount(ln.addr)
			if err != nil {
				r.decline("sender %x: %v", ln.addr, err)
				return
			}
			if base == nil {
				r.decline("tx %d: sender %x absent", i, ln.addr)
				return
			}
			ln.acc = *base
			normaliseLoaded(&ln.acc)
			if ln.acc.CodeHash != emptyCodeHash {
				r.decline("tx %d: sender %x has code", i, ln.addr)
				return
			}
			ln.loaded = true
		}
		a := &ln.acc
		if a.Nonce != pl.nonce || a.Nonce+1 < a.Nonce {
			r.decline("tx %d: nonce %d, sender's %d", i, pl.nonce, a.Nonce)
			return
		}
		feeCap.SetUint64(pl.feeCap)
		tip.SetUint64(pl.tip)
		value.SetUint64(pl.value)
		mgval.SetUint64(pl.gasPrice)
		mgval.Mul(&mgval, &gasU)
		balanceCheck.Mul(&feeCap, &gasU)
		balanceCheck.Add(&balanceCheck, &value)
		if a.Balance.Lt(&balanceCheck) {
			r.decline("tx %d: insufficient funds", i)
			return
		}
		// buyGas, the nonce bump, the transfer (the zero refund changes
		// nothing).
		a.Balance.Sub(&a.Balance, &mgval)
		a.Nonce++
		a.Balance.Sub(&a.Balance, &value)
		ln.last = i

		rc := &wk.receipts[k]
		rc.Type = pl.txType
		rc.Status = block.ReceiptStatusSuccessful
		rc.TxHash = pm.hash
		rc.GasUsed = gas
		rc.BlockNumber = hdr.Number
		rc.TransactionIndex = uint(i)

		// creditTransferFees under London, as tryDirectTransferPlan.
		effectiveTip := u256.Num0
		if feeCap.Gt(bp.baseFee) {
			effectiveTip = cmath.Min256(&tip, tipRoom.Sub(&feeCap, bp.baseFee))
		}
		amount.SetUint64(gas)
		amount.Mul(&amount, effectiveTip)
		fees[0] = deferredFee{recipient: bp.tipTo, amount: amount}
		nFees := 1
		if bp.burn {
			burn.SetUint64(gas)
			burn.Mul(&burn, bp.baseFee)
			fees[1] = deferredFee{recipient: *bp.burnTo, amount: burn}
			nFees = 2
		}
		res := &r.res[i]
		res.receipt, res.gasUsed, res.err, res.logs = rc, gas, nil, nil
		res.setFees(fees[:nFees])
	}
}

// publish is phase 4 on worker w: its lanes' final accounts (one full write
// each, at the lane's last transaction) and shard w's pure recipients (one
// balance delta each, at the last transaction that credited it).
func (r *laneRun) publish(w int) {
	wk := &r.wk[w]
	for li := range wk.lanes {
		ln := &wk.lanes[li]
		r.mvs.Write(parallel.LocationKey{Address: ln.addr, Field: parallel.FieldBalance}, ln.last, 0, ln.acc.MarshalV2())
	}
	for j := range r.recips[w] {
		rc := &r.recips[w][j]
		r.mvs.WriteDelta(parallel.LocationKey{Address: rc.addr, Field: parallel.FieldBalance}, rc.last, 0, &rc.sum)
	}
}

// runLanesOrRun is the run's executor step with the lanes switch on (mode
// lanesOn or lanesCheck): the lanes when they take the block, else Run. In
// check mode the lanes run into a scratch store and result slots, Run runs
// as without the switch, and the two are compared; Run's results are used.
func runLanesOrRun(mode int, executor *parallel.Executor, in *laneInputs, txResults []parallelTxResult) ([]parallel.TxResult, laneOutcome) {
	if mode == lanesOn {
		out := runTransferLanes(in, executor.MVS(), txResults)
		if out.used {
			transferLanesStats.used.Add(1)
			return executor.Results(), out
		}
		return executor.Run(), out
	}
	scratchMVS := parallel.NewMVS()
	scratch := make([]parallelTxResult, in.numTxs)
	out := runTransferLanes(in, scratchMVS, scratch)
	results := executor.Run()
	if out.used {
		diff := compareLaneRun(in.numTxs, scratchMVS, executor.MVS(), scratch, txResults, results)
		out.check = "same"
		if diff != "" {
			out.check = "diff"
		}
		noteLaneCheck(in.header.Number.Uint64(), diff)
	}
	out.used = false
	return results, out
}

// fields are the lanes' "parallel block" stamps.
func (o *laneOutcome) fields() []any {
	why := o.why
	if why == "" {
		why = "-"
	}
	check := o.check
	if check == "" {
		check = "-"
	}
	return []any{"lanes", transferLanesModeName(), "lanesUsed", o.used, "lanesWhy", why, "lanesCheck", check,
		"lanesN", o.lanes, "lanesTop", o.top, "lanesRecips", o.recipients,
		"lanesClassifyUs", o.classify.Microseconds(), "lanesMergeUs", o.merge.Microseconds(),
		"lanesRunUs", o.run.Microseconds(), "lanesPublishUs", o.publish.Microseconds()}
}

// laneFinal is one location's final version as the store's consumers read
// it (ApplyAll).
type laneFinal struct {
	value []byte
	delta *uint256.Int
}

// mvsFinals collects mvs's final versions by location.
func mvsFinals(mvs *parallel.MVS, numTxs int) (map[parallel.LocationKey]laneFinal, error) {
	out := make(map[parallel.LocationKey]laneFinal)
	err := mvs.ApplyAll(numTxs, func(key parallel.LocationKey, value []byte, delta *uint256.Int) error {
		out[key] = laneFinal{value: value, delta: delta}
		return nil
	})
	return out, err
}

// compareLaneRun is the check mode's comparison of a lane run (laneMVS,
// laneRes) with today's run of the same block (oldMVS, oldRes, results):
// every transaction's outcome and result slot, and every final version of
// the store. It returns the first difference, "" when there is none.
func compareLaneRun(numTxs int, laneMVS, oldMVS *parallel.MVS, laneRes, oldRes []parallelTxResult, results []parallel.TxResult) string {
	for i := 0; i < numTxs; i++ {
		if i < len(results) && results[i].Err != nil {
			return fmt.Sprintf("tx %d failed on today's path: %v", i, results[i].Err)
		}
		a, b := &laneRes[i], &oldRes[i]
		switch {
		case a.err != nil || b.err != nil:
			return fmt.Sprintf("tx %d error: lanes %v, today %v", i, a.err, b.err)
		case a.gasUsed != b.gasUsed:
			return fmt.Sprintf("tx %d gas: lanes %d, today %d", i, a.gasUsed, b.gasUsed)
		case len(a.logs) != 0 || len(b.logs) != 0:
			return fmt.Sprintf("tx %d logs: lanes %d, today %d", i, len(a.logs), len(b.logs))
		case (a.receipt == nil) != (b.receipt == nil):
			return fmt.Sprintf("tx %d receipt presence: lanes %v, today %v", i, a.receipt != nil, b.receipt != nil)
		case a.receipt != nil && !reflect.DeepEqual(*a.receipt, *b.receipt):
			return fmt.Sprintf("tx %d receipt: lanes %+v, today %+v", i, *a.receipt, *b.receipt)
		case len(a.fees) != len(b.fees):
			return fmt.Sprintf("tx %d fees: lanes %d, today %d", i, len(a.fees), len(b.fees))
		}
		for k := range a.fees {
			if a.fees[k].recipient != b.fees[k].recipient || a.fees[k].amount != b.fees[k].amount {
				return fmt.Sprintf("tx %d fee %d: lanes %x %s, today %x %s", i, k, a.fees[k].recipient, a.fees[k].amount.Hex(), b.fees[k].recipient, b.fees[k].amount.Hex())
			}
		}
	}
	lf, err := mvsFinals(laneMVS, numTxs)
	if err != nil {
		return fmt.Sprintf("lane store: %v", err)
	}
	of, err := mvsFinals(oldMVS, numTxs)
	if err != nil {
		return fmt.Sprintf("today's store: %v", err)
	}
	if len(lf) != len(of) {
		return fmt.Sprintf("store locations: lanes %d, today %d", len(lf), len(of))
	}
	for k, o := range of {
		l, ok := lf[k]
		switch {
		case !ok:
			return fmt.Sprintf("store %x field %d: missing from the lanes", k.Address, k.Field)
		case !bytes.Equal(l.value, o.value) || (l.value == nil) != (o.value == nil):
			return fmt.Sprintf("store %x field %d value: lanes %x, today %x", k.Address, k.Field, l.value, o.value)
		case (l.delta == nil) != (o.delta == nil) || (l.delta != nil && !l.delta.Eq(o.delta)):
			return fmt.Sprintf("store %x field %d delta: lanes %v, today %v", k.Address, k.Field, l.delta, o.delta)
		}
	}
	return ""
}

// noteLaneCheck counts and reports a check mode comparison; a difference
// panics in test binaries.
func noteLaneCheck(number uint64, diff string) {
	transferLanesStats.checked.Add(1)
	if diff == "" {
		return
	}
	transferLanesStats.mismatches.Add(1)
	log.Error("transfer lanes check: lanes differ from today's path; today's result used", "n", number, "diff", diff)
	if testing.Testing() {
		panic("transfer lanes check: " + diff)
	}
}
