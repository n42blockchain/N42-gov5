// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Parallel state transition processor. Splits a block's transactions
// into conflict-free batches based on sender and access list,
// executes each batch in its own EVM instance and merges the
// resulting state deltas. Falls back to sequential execution on
// conflict or error.

package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// parallelWorkers is the Block-STM worker count: N42_PARALLEL_WORKERS, default
// 32. One read transaction per worker is a different resource than one
// goroutine per worker (94775a0a), so this is not NumCPU (256 here).
func parallelWorkers() int {
	if v := os.Getenv("N42_PARALLEL_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 32
}

// parallelTxResult stores per-transaction execution output from parallel execution.
type parallelTxResult struct {
	receipt *block.Receipt
	gasUsed uint64
	err     error
	logs    []*block.Log
	fees    []deferredFee
	// feeBuf holds fees in place for the usual one or two credits, so a
	// transaction's fees cost no allocation.
	feeBuf [2]deferredFee
}

// setFees stores a copy of fees in the slot.
func (r *parallelTxResult) setFees(fees []deferredFee) {
	switch {
	case len(fees) == 0:
		r.fees = nil
	case len(fees) <= len(r.feeBuf):
		n := copy(r.feeBuf[:], fees)
		r.fees = r.feeBuf[:n:n]
	default:
		r.fees = append([]deferredFee(nil), fees...)
	}
}

// txResultsFree keeps a block's worth of parallelTxResult slots across blocks.
// A fresh 163k-element slice per block is ~10 MB the runtime hands back to
// the OS under the memory limit and page-faults in again on the next block;
// with the executor arena in place that fault was the whole 60 ms "executorMs"
// of a follower's import (round 35zza). Two slots: import and builder.
var txResultsFree struct {
	mu   sync.Mutex
	list [][]parallelTxResult
}

func takeTxResults(n int) []parallelTxResult {
	txResultsFree.mu.Lock()
	defer txResultsFree.mu.Unlock()
	for i := len(txResultsFree.list) - 1; i >= 0; i-- {
		if r := txResultsFree.list[i]; cap(r) >= n {
			txResultsFree.list = append(txResultsFree.list[:i], txResultsFree.list[i+1:]...)
			// Zero up to its capacity already: giveTxResults cleared it,
			// and what lies past its last length was never used since.
			return r[:n]
		}
	}
	return make([]parallelTxResult, n)
}

func giveTxResults(r []parallelTxResult) {
	clear(r) // drop the receipt and log pointers now; takeTxResults relies on it
	txResultsFree.mu.Lock()
	defer txResultsFree.mu.Unlock()
	if len(txResultsFree.list) < 2 {
		txResultsFree.list = append(txResultsFree.list, r[:0])
		return
	}
	// Full: replace the smallest kept slice if this one is larger, so two
	// small slices from a leg's ramp cannot hold the list for the run (the
	// same trap as the executor arena's free list, round 35zzh).
	smallest := 0
	for i, k := range txResultsFree.list {
		if cap(k) < cap(txResultsFree.list[smallest]) {
			smallest = i
		}
	}
	if cap(txResultsFree.list[smallest]) < cap(r) {
		txResultsFree.list[smallest] = r[:0]
	}
}

// deferredFee is a block-producer credit a transaction diverted to its fee
// sink; ProcessParallel sums them and credits the state once per recipient.
type deferredFee struct {
	recipient types.Address
	amount    uint256.Int
}

// deferredFeeRecipients lists the accounts whose credits ProcessParallel
// defers to the end of the block: the priority-fee recipient and, when the
// chain collects the base fee, the collector.
func deferredFeeRecipients(cfg *params.ChainConfig, header *block.Header) []types.Address {
	policy := newTransitionChainPolicy(cfg)
	recipients := []types.Address{policy.priorityFeeRecipient(header.Coinbase)}
	if cfg != nil && cfg.Eip1559FeeCollector != nil {
		if c := *cfg.Eip1559FeeCollector; c != recipients[0] {
			recipients = append(recipients, c)
		}
	}
	return recipients
}

// touchesAny reports whether any transaction sends from or to one of the
// addresses. Such a transaction would observe a coinbase balance that the
// deferred credit has not yet reached, so the block must run sequentially.
func touchesAny(txs []*transaction.Transaction, addrs []types.Address) bool {
	if len(txs) >= touchesAnyParallelMin {
		return touchesAnyParallel(txs, addrs)
	}
	return touchesAnyRange(txs, addrs)
}

// touchesAnyParallelMin is the list size from which touchesAny splits the
// scan across goroutines (P10: the serial scan of a 163k block was ~6.6 ms
// on the fleet, twice per ahead run, all cache misses on the transactions).
const touchesAnyParallelMin = 8192

func touchesAnyParallel(txs []*transaction.Transaction, addrs []types.Address) bool {
	workers := len(txs) / touchesAnyParallelMin
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		return touchesAnyRange(txs, addrs)
	}
	per := (len(txs) + workers - 1) / workers
	var hit atomic.Bool
	var wg sync.WaitGroup
	for lo := 0; lo < len(txs); lo += per {
		hi := min(lo+per, len(txs))
		wg.Add(1)
		go func(part []*transaction.Transaction) {
			defer wg.Done()
			if touchesAnyRange(part, addrs) {
				hit.Store(true)
			}
		}(txs[lo:hi])
	}
	wg.Wait()
	return hit.Load()
}

func touchesAnyRange(txs []*transaction.Transaction, addrs []types.Address) bool {
	for _, tx := range txs {
		for _, a := range addrs {
			if from := tx.From(); from != nil && *from == a {
				return true
			}
			if to := tx.To(); to != nil && *to == a {
				return true
			}
		}
	}
	return false
}

// ProcessParallel executes all transactions in the block using Block-STM
// wave-based parallel execution. It falls back to sequential execution
// when the block has too few transactions or when parallel execution
// detects excessive conflicts.
//
// The approach:
//  1. Execute all txs in parallel, each with its own IntraBlockState and
//     ParallelStateReader (reads from MVS) / ParallelStateWriter (captures writes).
//  2. Validate read sets using the wave-based executor.
//  3. After validation, replay the validated state changes to the real
//     IntraBlockState (ibs) so that CommitBlock can flush them to the DB.
//
// Note: the stateWriter parameter may be a NoopWriter (from evmRecord). All
// state changes MUST be applied to ibs, which CommitBlock later flushes to
// the real database writer.
// ErrParallelNotApplicable says a lenient (builder) run cannot use the
// parallel path for this transaction list; the caller runs its serial fill.
var ErrParallelNotApplicable = errors.New("parallel build not applicable")

// parallelRun is the outcome of one Block-STM execution of a transaction
// list. In strict mode every transaction succeeded; in lenient mode the
// failed ones (a nonce, funds or gas pre-check) are dropped -- they wrote
// nothing, so the state is exactly what the survivors alone produce -- and
// Included/Receipts hold the survivors in order with cumulative gas and
// indices renumbered.
type parallelRun struct {
	Included []*transaction.Transaction
	Receipts block.Receipts
	Logs     []*block.Log
	UsedGas  uint64
	Failed   int
	Skipped  int // frame skip rule (skipInvalid runs only)
	// SkipReasons splits Skipped by reason.
	SkipReasons FrameSkipCounts
	Nopay       map[types.Address]*uint256.Int
	// phases: an ahead run's breakdown (FinishParallel adds Finalize).
	phases ProcessPhases
}

// ProcessParallel executes a block's transactions with Block-STM (strict:
// any transaction failure fails the block) and finalizes it.
func (p *StateProcessor) ProcessParallel(b *block.Block, ibs *state.IntraBlockState, stateReader state.StateReader, stateWriter state.WriterWithChangeSets, blockHashFunc func(n uint64) types.Hash) (block.Receipts, map[types.Address]*uint256.Int, []*block.Log, uint64, error) {
	txs := b.Transactions()
	if len(txs) <= 4 {
		return p.Process(b, ibs, stateReader, stateWriter, blockHashFunc)
	}
	concreteHeader, ok := b.Header().(*block.Header)
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("ProcessParallel: invalid header type assertion for block %v", b.Number64())
	}
	if touchesAny(txs, deferredFeeRecipients(p.config, concreteHeader)) {
		// A block that sends from or to a fee recipient cannot defer the
		// credit (see runParallel) and runs sequentially.
		return p.Process(b, ibs, stateReader, stateWriter, blockHashFunc)
	}
	body, _ := b.Body().(*block.Body)
	run, err := p.runParallel(concreteHeader, b.Hash(), txs, ibs, blockHashFunc, false, body.IsFrameBody(), FrameSpansOf(body, len(txs)))
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return run.Receipts, run.Nopay, run.Logs, run.UsedGas, nil
}

// ParallelApplicable reports whether ProcessParallel runs b on Block-STM
// (rather than falling back to the serial Process).
func (p *StateProcessor) ParallelApplicable(b *block.Block) bool {
	txs := b.Transactions()
	if len(txs) <= 4 {
		return false
	}
	hdr, ok := b.Header().(*block.Header)
	return ok && !touchesAny(txs, deferredFeeRecipients(p.config, hdr))
}

// ExecuteParallelAhead is the first half of ProcessParallel for the lag
// executor's pipeline: the strict Block-STM run of b and the fold of its
// writes into ibs, with no block end, no Finalize and no state root. Its
// reads take the QMDB tree's reader lock per lookup (never for the whole
// run; a key its base prefill holds takes none, ahead_base_prefill.go) and
// retry an entry evicted under them through a fresh transaction,
// so the parent's root and write proceed on the tree meanwhile. ibs reads
// the parent's unwritten post-state through its post-state layers.
// FinishParallel completes the block once its parent is written.
func (p *StateProcessor) ExecuteParallelAhead(b *block.Block, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (*parallelRun, error) {
	return p.executeParallelAhead(b, ibs, blockHashFunc, nil)
}

// executeParallelAhead is ExecuteParallelAhead with the block's preparation
// already claimed (nil: the run claims it).
func (p *StateProcessor) executeParallelAhead(b *block.Block, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash, claim *aheadClaim) (*parallelRun, error) {
	if !p.ParallelApplicable(b) {
		return nil, ErrParallelNotApplicable
	}
	hdr := b.Header().(*block.Header)
	body, _ := b.Body().(*block.Body)
	txs := b.Transactions()
	return p.runParallelMode(hdr, b.Hash(), txs, ibs, blockHashFunc, false, body.IsFrameBody(), true, FrameSpansOf(body, len(txs)), claim)
}

// aheadClaim is a block's preparation its ahead run claimed before building
// its readers (their base prefill comes with it).
type aheadClaim struct {
	prep *aheadPrep
	wait time.Duration
}

// FinishParallel is the second half: the gas check, block end and Finalize
// (rewards, the state root) of a run ExecuteParallelAhead left in ibs. The
// caller owns the live tree (it holds the chain lock, the parent is the
// applied head).
func (p *StateProcessor) FinishParallel(b *block.Block, ibs *state.IntraBlockState, run *parallelRun) (block.Receipts, map[types.Address]*uint256.Int, []*block.Log, uint64, error) {
	hdr, ok := b.Header().(*block.Header)
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("FinishParallel: invalid header type assertion for block %v", b.Number64())
	}
	t0 := time.Now()
	nopay, err := p.finishStrict(hdr, ibs, b.Transactions(), run.UsedGas)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	ph := run.phases
	ph.Finalize = time.Since(t0)
	p.lastPhases.Store(&ph)
	return run.Receipts, nopay, run.Logs, run.UsedGas, nil
}

// finishStrict is the strict tail of a parallel run: gas against the
// header, block end, Finalize.
func (p *StateProcessor) finishStrict(concreteHeader *block.Header, ibs *state.IntraBlockState, txs []*transaction.Transaction, usedGas uint64) (map[types.Address]*uint256.Int, error) {
	if !p.config.IsDeferredExecution(concreteHeader.Time) && usedGas != concreteHeader.GasUsed {
		return nil, fmt.Errorf("gas used by execution: %d, in header: %d", usedGas, concreteHeader.GasUsed)
	}
	if _, err := ProcessExecutionBlockEnd(nil, p.config, ibs, concreteHeader, p.engine); err != nil {
		return nil, err
	}
	_, nopay, err := p.engine.Finalize(p.bc, concreteHeader, ibs, txs, nil)
	if err != nil {
		return nil, err
	}
	return nopay, nil
}

// BuildParallel is the builder's lenient run over a candidate list: the
// header is the one being built (GasUsed unset), failed candidates are
// dropped, and neither block end nor Finalize runs -- the builder's assemble
// does that. ErrParallelNotApplicable when a candidate touches a fee
// recipient.
func (p *StateProcessor) BuildParallel(header *block.Header, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (*parallelRun, error) {
	if touchesAny(txs, deferredFeeRecipients(p.config, header)) {
		return nil, ErrParallelNotApplicable
	}
	return p.runParallel(header, types.Hash{}, txs, ibs, blockHashFunc, true, false, nil)
}

// ErrFrameTxNotSkippable: a frame build met a transaction failure the skip
// rule does not cover; nothing was applied and the builder falls back to
// its serial frame fill, which drops the offending frame.
var ErrFrameTxNotSkippable = errors.New("frame build: transaction failure outside the skip rule")

// BuildParallelFrames is the builder's run over the transactions of whole
// frames: like the import of a frame block, every transaction stays in the
// block and skip-rule failures get skip receipts. Any other failure returns
// ErrFrameTxNotSkippable before the state is touched.
//
// spans (optional) name the frames txs decode from, so their senders are
// recovered under the frame store's claims (P8a).
func (p *StateProcessor) BuildParallelFrames(header *block.Header, txs []*transaction.Transaction, spans []FrameSpan, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (*parallelRun, error) {
	if touchesAny(txs, deferredFeeRecipients(p.config, header)) {
		return nil, ErrParallelNotApplicable
	}
	return p.runParallel(header, types.Hash{}, txs, ibs, blockHashFunc, true, true, spans)
}

// runParallel is the shared core: sender gate, per-worker readers, the
// executor, the fold into ibs, the deferred fee credit; strict mode then
// validates gas against the header and finalizes.
//
// skipInvalid applies the frame-block skip rule (frame_exec.go): a
// skip-rule failure keeps its transaction with a skip receipt and fails
// neither mode; in lenient mode any other failure returns
// ErrFrameTxNotSkippable before the state is applied.
func (p *StateProcessor) runParallel(concreteHeader *block.Header, blockHash types.Hash, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash, lenient, skipInvalid bool, spans []FrameSpan) (*parallelRun, error) {
	return p.runParallelMode(concreteHeader, blockHash, txs, ibs, blockHashFunc, lenient, skipInvalid, false, spans, nil)
}

// runParallelMode is runParallel; ahead (strict runs only) stops after the
// fold, before block end and Finalize, and reads the QMDB tree without
// holding its reader lock across the run (ExecuteParallelAhead).
func (p *StateProcessor) runParallelMode(concreteHeader *block.Header, blockHash types.Hash, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash, lenient, skipInvalid, ahead bool, spans []FrameSpan, claim *aheadClaim) (*parallelRun, error) {
	numTxs := len(txs)
	// An ahead run claims the block's preparation (ahead_prep.go) before
	// its sender recovery below writes any sender: one still running is
	// waited for, one not started is cancelled. The lag pipeline claims it
	// before building the run's state reader (claim), for its base prefill.
	var prep *aheadPrep
	var prepWait time.Duration
	if ahead && p.bc != nil {
		if claim != nil {
			prep, prepWait = claim.prep, claim.wait
		} else {
			prep, prepWait = p.bc.claimAheadPrep(blockHash)
		}
	}
	// The base prefill (ahead_base_prefill.go): the run's tree reads answer
	// its keys without the tree's lock.
	base := prep.prefill()
	var basePF *commitment.BasePrefill
	if ahead && base != nil && (claim != nil || base.depth <= 1) {
		// A prefill read two or more blocks below the parent needs the
		// layers the lag pipeline's claim sets up (aheadBaseLayers).
		basePF = base.pf
	}
	tStart := time.Now()
	senderHintHits := 0
	senderHintFills := 0

	// Same consensus-safety gate as the serial Process: this executor also
	// reaches AsMessage, which trusts a wire-declared `From`. Without it a
	// parallel-EVM node would accept a block a sequential node rejects —
	// worse than the original hole, because the fleet splits instead of
	// agreeing. (Blocks of <=4 txs took the gated Process path above.)
	var signer transaction.Signer
	if hdrNum, hdrErr := requireHeaderNumber(concreteHeader, "header number unavailable"); hdrErr == nil {
		signer = transaction.MakeSignerWithTimestamp(p.config, hdrNum.ToBig(), concreteHeader.Time)
		var hints SenderHintSource
		if p.bc != nil {
			hints = p.bc.senderHints
		}
		// A frame block whose senders were all attached before it executes
		// (frame resolution or its sender job, P10) has nothing to recover;
		// unless a transaction declares its own sender, the gate below has
		// nothing to check either.
		sendersDone, declared := false, true
		if spans != nil && p.bc != nil {
			sendersDone, declared = p.bc.takeSendersDone(blockHash, signer, txs)
		}
		if declared {
			hits, err := verifyBlockSendersHinted(signer, txs, hints)
			if err != nil {
				return nil, fmt.Errorf("block %s: %w", concreteHeader.Number.String(), err)
			}
			senderHintHits = hits
		}
		// The pool already recovered the sender of every transaction it
		// holds; copy those onto the wire copies first (the serial import
		// path has done this since the hint source existed; the parallel
		// path only ran the gate, which is inert for wire transactions).
		// Round 35z4: recovery was 321 ms of a follower's 1.25 s import at
		// 163k transactions, on the round's critical path (r2 waits for the
		// import), with every one of those transactions sitting in the pool.
		// A frame block (spans) takes its senders frame by frame from the
		// frame store and the attestations below, in bulk; the per-
		// transaction hint pass found nothing there (S101: 587 of 589
		// imports filled 0) and cost 6.8% of a node (hash, cache probe and
		// a pool read lock per transaction).
		if spans == nil {
			senderHintFills = applySenderHints(hints, signer, txs)
		}
		// A block off the wire carries RLP only: From() is nil until the
		// signature is recovered. The affinity key below needs every sender
		// before the first wave, so recover the rest here in parallel
		// (memoised on the transaction; AsMessage reuses it). Round 35d/t:
		// with From() nil the key fell back to the index, a sender's nonce
		// chain ran on 32 workers at once, and every link but the first
		// failed its nonce check wave after wave until the 64-wave limit.
		// P7: the block's sender job (sender_prewarm.go) started when it was
		// queued; the import joins it and waits only for what is left.
		if sendersDone {
			p.bc.dropSenderJob(blockHash)
		} else {
			p.bc.recoverBlockSendersFor(blockHash, signer, txs, spans)
		}
	}
	tRecovered := time.Now()

	chainConfig := p.config
	cfg := vm2.Config{}

	// The fee credit is the one write every transaction shares. Deferred to
	// the end of the block it leaves Block-STM only the real conflicts; a
	// block that sends from or to a fee recipient cannot defer (the credit
	// would be visible to those transactions in serial order) and runs
	// sequentially. Contract calls that read the coinbase balance (COINBASE +
	// BALANCE) are not covered by the address scan; the benchmark workload is
	// plain transfers, and an EVM-visible difference shows as a BAD BLOCK.
	feeRecipients := deferredFeeRecipients(chainConfig, concreteHeader)

	// blockContext is a value type — each goroutine's NewEVM copies it, safe to share.
	blockContext := NewEVMBlockContext(concreteHeader, blockHashFunc, p.engine, chainConfig, nil)

	// Per-tx result storage. Each goroutine writes to its own index (no race).
	tBlockStart := time.Now()
	txResults := takeTxResults(numTxs)
	// Cleared and put back off this goroutine: clearing a 163k block's
	// results (~30 MB) was ~3 ms at the end of every run (P10). Nothing
	// reads them after the run returns.
	defer func() { go giveTxResults(txResults) }()

	// Every worker owns its base reader. 3709ca6a proved the shared one was a
	// shared MDBX cursor (a read transaction is bound to the OS thread that
	// opened it, and GetOne pulls a per-bucket cursor out of an unsynchronised
	// map), and 94775a0a set the shape of the fix: a read transaction opened
	// on the worker's own goroutine, a worker count chosen for transactions
	// rather than for CPUs, and every worker reading the SAME snapshot. The
	// snapshot is the live QMDB tree at the parent, static for the whole
	// execution (its owner is this goroutine, which is waiting), read through
	// LookupSource under the tree's reader lock with the worker's own
	// transaction as the cold getter; code and storage rows come from that
	// same transaction. A worker whose transaction cannot be opened fails its
	// transactions, and the block, rather than reading through anything shared.
	mode := commitment.QMDBStateReadMode()
	useQMDB := mode != commitment.QMDBReadOff && p.bc != nil && p.bc.qmdbEnabled && p.bc.qmdbRootComputer != nil
	if p.bc == nil || p.bc.ChainDB == nil {
		return nil, fmt.Errorf("ProcessParallel: no chain database for per-worker readers")
	}
	// A worker owns one IntraBlockState, one EVM, one reader and one writer
	// for the whole block and rebinds them per transaction: allocating them
	// per transaction was 46% of the executor's CPU in round 35g's profile
	// (mallocgc under 32 workers serialises on the heap lock, and the
	// garbage drives the GC), with the workers running at ~7 cores of 32.
	type workerCtx struct {
		tx       kv.Tx
		base     state.StateReader
		reader   *parallel.ParallelStateReader
		writer   *parallel.ParallelStateWriter
		ibs      *state.IntraBlockState
		evm      *vm2.EVM
		observed map[types.Address]struct{}
		// Per-transaction scratch, reused across the worker's
		// transactions: the message, the fast path's values, the gas
		// pool, and the fee sink with its buffer (copied out per
		// transaction into the result slot).
		scratch parallelTxScratch
		gp      common.GasPool
		fees    []deferredFee
		sink    FeeSink
		// ibsUsed: a transaction ran on ibs since its last Reset.
		ibsUsed bool
	}
	// A chained speculative build reads its unwritten own parents through
	// post-state snapshots layered on the block's reader; the per-worker
	// readers below open their own store transactions and must carry the
	// same layers, or the workers execute against a store that lacks the
	// parent (round 35zu: the faucet's funding block lost the parent's
	// reward and every follower rejected it on the state root). Followers
	// and unchained builds have no layers; this is a no-op for them.
	postLayers := ibs.PostStateLayers()
	if postLayers == nil {
		postLayers, _ = state.PostStateLayers(ibs.GetStateReader())
	}
	var executor *parallel.Executor
	// Each transaction's message and hash, built on the executor's parallel
	// affinity-key pass: walking the transactions in index order there
	// costs less than the same reads in the wave, where a worker visits
	// them in sender order (AsMessageInto and tx.Hash were ~15% of the
	// wave's CPU at 163k transfers, almost all cache misses on the
	// transaction's objects). Fresh per block: a kept slice (cleared on
	// release, like the results') measured slower, the collector marking
	// it on every cycle.
	//
	// An ahead run adopts the ones its preparation built for these
	// transactions (ahead_prep.go).
	//
	// The run owns its preparation: a claimed one (adopted or not) is no
	// longer read by its base prefill (the claim waited for it), and the
	// transaction array goes back for reuse once the run returns
	// (preparedSet.release).
	var prepared *preparedSet
	prepAdopted := prep.matches(txs, signer, concreteHeader.BaseFee, p.engine != nil) && !cfg.StatelessExec
	if prep != nil {
		if prep.prepared != nil {
			if prepAdopted {
				prepared = prep.prepared
			} else {
				defer prep.prepared.release()
			}
		}
		prep.prepared = nil
	}
	if prepared == nil {
		prepared = newPreparedSet(numTxs)
	}
	defer prepared.release()
	// One base-read cache for this block, shared by every worker (see
	// parallel.BaseCache). Sized for the distinct accounts a block touches:
	// the bench's blocks are ~163k transfers over ~23k recipients.
	baseCache := parallel.NewBaseCache(len(txs)/4 + 64)
	// The QMDB run holds the tree's reader lock (span below): its workers
	// open their transactions under the span's permit, taken before the
	// lock, and never wait on the limiter while the lock is held.
	var span *commitment.ReadSpan
	setup := func(workerID int) (any, func(), error) {
		var tx kv.Tx
		var err error
		if span != nil {
			tx, err = span.BeginRo(context.Background())
		} else {
			tx, err = p.bc.ChainDB.BeginRo(context.Background())
		}
		if err != nil {
			return nil, nil, fmt.Errorf("parallel worker %d: open read transaction: %w", workerID, err)
		}
		base := p.parallelBaseReader(tx, useQMDB, ahead, mode, postLayers, basePF)
		wc := &workerCtx{tx: tx, base: base, observed: make(map[types.Address]struct{}, 8)}
		wc.reader = parallel.NewParallelStateReader(base, executor.MVS(), nil, 0)
		// One base-read cache for the whole block, shared by every worker: the
		// base is the parent's post-state and cannot change while the block
		// executes, and each transaction otherwise pays its own read of the
		// accounts it touches (~163k reads for ~23k accounts at the bench's
		// block size).
		wc.reader.SetBaseCache(baseCache)
		wc.reader.EnableEntryCache()
		// Its only consumer is the worker's IntraBlockState, which copies
		// every account it reads.
		wc.reader.ReuseAccountBuffer()
		wc.ibs = state.New(wc.reader)
		wc.writer = parallel.NewParallelStateWriter(nil)
		// The worker's recorded values come from one slab.
		slab := parallel.NewValueSlab()
		wc.reader.SetSlab(slab)
		wc.writer.SetSlab(slab)
		// Every explicit balance read (GetBalance, Empty) of a transaction
		// is recorded: an account it credits without ever observing gets a
		// delta write, and its read of that account (for code, nonce) is
		// validated on every field but the balance.
		wc.ibs.SetBalanceReadHook(func(a types.Address) { wc.observed[a] = struct{}{} })
		wc.writer.SetDeltaEligible(func(a types.Address) bool { _, seen := wc.observed[a]; return !seen })
		wc.evm = vm2.NewEVM(blockContext, evmtypes.TxContext{}, wc.ibs, chainConfig, cfg)
		wc.sink = func(recipient types.Address, amount *uint256.Int) {
			wc.fees = append(wc.fees, deferredFee{recipient: recipient, amount: *amount})
		}
		return wc, func() { wc.scratch.fast.flush(); tx.Rollback() }, nil
	}
	executor = parallel.NewExecutorWithWorkerSetup(numTxs, parallelWorkers(), setup, func(ctx any, txIndex int, rw *parallel.ReadWriteSet) error {
		tx := txs[txIndex]
		wc, ok := ctx.(*workerCtx)
		if !ok || wc == nil || wc.base == nil {
			return fmt.Errorf("parallel worker has no state reader for tx %d", txIndex)
		}

		// Rebind the worker's state to this transaction: the reader serves
		// its MVS view, the writer records into its read/write set, the
		// IntraBlockState forgets the previous transaction's objects.
		wc.reader.Rebind(rw, txIndex)
		wc.writer.Rebind(rw)
		wc.fees = wc.fees[:0]
		r := &txResults[txIndex]
		wc.scratch.msgReady = false
		if pm := &prepared.txs[txIndex]; pm.ok {
			if transferFast2 && transferFastPath && transferDirectPath && pm.plan.ok {
				if res, ok := tryDirectTransferPlan(wc.evm, wc.reader, wc.writer, rw, &pm.plan, concreteHeader.GasLimit, wc.sink, &wc.scratch.fast); ok {
					var receipt *block.Receipt
					if !cfg.NoReceipts {
						receipt = wc.scratch.transferReceipt(pm.plan.txType, pm.hash, res, concreteHeader, uint(txIndex))
					}
					r.receipt, r.gasUsed, r.err, r.logs = receipt, res.BlockGasUsed, nil, nil
					r.setFees(wc.fees)
					return nil
				}
			}
			// The message: kept by the preparation, else rebuilt into the
			// worker's scratch exactly as the preparation built it.
			msg := prepared.keptMsg(txIndex)
			if msg == nil && p.buildPreparedMessage(&wc.scratch.msg, tx, signer, concreteHeader.BaseFee, cfg.StatelessExec) {
				msg = &wc.scratch.msg
			}
			if msg != nil {
				if transferFastPath && transferDirectPath {
					// A plain transfer straight on the reader and writer,
					// without the IntraBlockState (tryDirectTransfer).
					if res, ok := tryDirectTransfer(wc.evm, wc.reader, wc.writer, rw, msg, concreteHeader.GasLimit, wc.sink, &wc.scratch.fast); ok {
						var receipt *block.Receipt
						if !cfg.NoReceipts {
							receipt = wc.scratch.fillReceipt(false, tx, pm.hash, res, msg, nil, concreteHeader, uint(txIndex), wc.evm)
						}
						r.receipt, r.gasUsed, r.err, r.logs = receipt, res.BlockGasUsed, nil, nil
						r.setFees(wc.fees)
						return nil
					}
				}
				// The EVM path may adjust its message: it gets a copy.
				if msg != &wc.scratch.msg {
					wc.scratch.msg = *msg
				}
				wc.scratch.msgReady = true
			}
		}
		// The IntraBlockState is reset only once a transaction has used it
		// since its last reset: direct transfers leave it untouched.
		if wc.ibsUsed {
			wc.ibs.Reset()
			clear(wc.observed)
		}
		wc.ibsUsed = true
		txIBS, pWriter := wc.ibs, wc.writer

		txIBS.Prepare(tx.Hash(), blockHash, txIndex)

		// Each tx gets its own gas pool (block-level gas validation happens after).
		gp := &wc.gp
		*gp = 0
		gp.AddGas(concreteHeader.GasLimit)

		vmenv := wc.evm

		receipt, gasUsed, logs, err := parallelApplyTx(chainConfig, p.engine, gp, txIBS, pWriter, concreteHeader, tx, vmenv, cfg, wc.sink, signer, &wc.scratch)
		rw.MarkBalanceInsensitive(func(a types.Address) bool { _, seen := wc.observed[a]; return seen })

		// Field by field: the slot's fee buffer must survive the write.
		r.receipt, r.gasUsed, r.err, r.logs = receipt, gasUsed, err, logs
		r.setFees(wc.fees)

		return err
	})

	// Pin each sender's transactions to one worker, in index order: the
	// nonce chain of a sender is then executed by the worker that already
	// applied its predecessor, and only cross-sender conflicts (a recipient
	// credited by several senders in one wave) reach the validator.
	//
	// The executor computes the keys in parallel, in index order, before
	// the wave starts; the key function also prepares each transaction's
	// message and hash there (prepared, above).
	executor.SetAffinity(func(txIndex int) uint64 {
		return p.txAffinityKey(txs, prepared, txIndex, signer, concreteHeader.BaseFee, cfg.StatelessExec)
	})
	// The keys, messages and hashes need no state: they are prepared before
	// the block-start system calls, whose reads of the tree wait while the
	// parent's root holds it (an ahead run starts at the parent's snapshot,
	// just before its ComputeRoot), so the preparation overlaps that wait
	// instead of following it (P10).
	//
	// An adopted preparation computed them already, for this executor's
	// worker count and wave modes (AdoptAffinity checks).
	prepHit := prepAdopted && executor.AdoptAffinity(prep.aff)
	if !prepHit {
		executor.PrepareAffinity()
	}
	if ahead {
		if prepHit {
			aheadPrepHits.Add(1)
		} else {
			aheadPrepMisses.Add(1)
		}
	}
	prep = nil
	tAffinity := time.Now()
	if err := ProcessExecutionBlockStart(concreteHeader.ParentBeaconRoot, chainConfig, ibs, concreteHeader, p.engine); err != nil {
		executor.Release()
		return nil, err
	}
	// Run parallel execution with wave-based validation. The tree's reader
	// lock is held once for the whole run (the workers' lookups skip it);
	// nothing in the run takes the writer side -- the owner is this
	// goroutine.
	tExecutorMade := time.Now()
	tRunStart := time.Now()
	// S81 measurement-only snapshot: coldReads/residentHits/coldNanos
	// (lib/qmdb/evict.go) are process-wide atomics, but in production the
	// ONLY caller that reaches Tree.GetVia (the function that bumps them) is
	// this wave's own per-worker LookupSource -- see docs/QS_EXEC_WAVE_WAIT.md
	// and the confirmation note below. Taking a before/after snapshot across
	// executor.Run() therefore isolates this block's wave reads WITHOUT
	// resetting the counters, which would be unsafe: a leader's isolated
	// build tree and a follower's live tree can both be bumping the same
	// atomics in the same process. Caveat: if a future caller ever reaches
	// GetVia from outside the wave (e.g. a second concurrent block build),
	// this snapshot would double-count that caller's reads too. Two such
	// callers exist on the ahead path (P15): a base prefill answers its
	// keys without reaching GetVia (they are not counted here), and the
	// next block's prefill may read the tree while this wave runs (counted).
	var waveCold0, waveResident0 uint64
	var waveColdNs0 int64
	if useQMDB {
		waveCold0, waveResident0 = qmdb.ReadCounters()
		waveColdNs0 = qmdb.ReadColdNanos()
	}
	if useQMDB && !ahead {
		var err error
		if span, err = p.bc.qmdbRootComputer.BeginReadSpan(context.Background(), p.bc.ChainDB); err != nil {
			return nil, fmt.Errorf("ProcessParallel: read span: %w", err)
		}
	}
	var waveCPU0 time.Duration
	if waveCPUMeter != nil {
		waveCPU0 = waveCPUMeter()
	}
	results := executor.Run()
	if waveCPUMeter != nil {
		waveCPUNs.Add(int64(waveCPUMeter() - waveCPU0))
	}
	if span != nil {
		span.End()
		span = nil
	}
	tRunEnd := time.Now()
	var waveReads, waveColdReads uint64
	var waveColdMs int64
	if useQMDB {
		cold1, resident1 := qmdb.ReadCounters()
		waveColdReads = cold1 - waveCold0
		waveReads = waveColdReads + (resident1 - waveResident0)
		waveColdMs = (qmdb.ReadColdNanos() - waveColdNs0) / 1e6
	}
	waveBusyMs := executor.BusyNanos() / 1e6
	wkStats := executor.WorkerStats() // S82: per-worker wall/queue/setup/mvs timings for this block's waves
	waveWorkNs.Add(executor.BusyNanos() + wkStats.MvsWriteNs)

	// Strict: any transaction failure fails the block. Lenient: a failed
	// candidate wrote nothing (the failure is a pre-check, before FinalizeTx),
	// so it is dropped and the survivors are renumbered.
	failed := 0
	// Why the lenient run dropped what it dropped. Round 35r's A2 leg had a
	// third of the leader fills execute 22,857 candidates and drop every one,
	// with the stale-nonce trim finding nothing -- and no record of the
	// reason. Counted by class so the log line answers that next time.
	var failNonceLow, failNonceHigh, failFunds, failFeeCap, failOther int
	var failSample error
	var skipped []bool
	nSkipped := 0
	var skipReasons FrameSkipCounts
	for i, r := range results {
		if r.Err == nil {
			continue
		}
		if skipInvalid {
			if IsFrameSkippable(r.Err, txs[i], signer) {
				if skipped == nil {
					skipped = make([]bool, numTxs)
				}
				skipped[i] = true
				nSkipped++
				skipReasons.Add(r.Err)
				continue
			}
			if lenient {
				executor.Release()
				return nil, fmt.Errorf("%w: tx %d [%v]: %v", ErrFrameTxNotSkippable, i, txs[i].Hash().String(), r.Err)
			}
		}
		if !lenient {
			return nil, fmt.Errorf("could not apply tx %d from block %d [%v]: %w",
				i, concreteHeader.Number.Uint64(), txs[i].Hash().String(), r.Err)
		}
		failed++
		switch {
		case errors.Is(r.Err, ErrNonceTooLow):
			failNonceLow++
		case errors.Is(r.Err, ErrNonceTooHigh):
			failNonceHigh++
			if failNonceHigh <= 2 && lenient {
				// Round 35v A legs: after every full block the next fill
				// dropped all 22,857 candidates as nonce-too-high while the
				// stale-nonce trim (the build's own reader) had just removed
				// the previous block's mined prefix. The worker's error text
				// carries the nonce it saw; read the same sender through the
				// build's reader so the two views sit side by side.
				var buildNonce int64 = -1
				if from := txs[i].From(); from != nil {
					if acc, aerr := ibs.GetStateReader().ReadAccountData(*from); aerr == nil && acc != nil {
						buildNonce = int64(acc.Nonce)
					} else if aerr == nil {
						buildNonce = 0
					}
				}
				// Every candidate of this sender in the list, with its outcome:
				// tells a missing head (pool gap) from a head whose write the
				// same worker's next transaction did not see (executor).
				chain := ""
				if from := txs[i].From(); from != nil {
					n := 0
					for j := range txs {
						if f := txs[j].From(); f == nil || *f != *from {
							continue
						}
						st := "ok"
						if results[j].Err != nil {
							st = results[j].Err.Error()
							if len(st) > 24 {
								st = st[:24]
							}
						}
						chain += fmt.Sprintf(" [%d:%d %s]", j, txs[j].Nonce(), st)
						if n++; n >= 5 {
							break
						}
					}
				}
				log.Info("parallel fill nonce-high sample", "n", concreteHeader.Number.Uint64(), "tx", i,
					"txNonce", txs[i].Nonce(), "buildReaderNonce", buildNonce, "workerErr", r.Err, "senderChain", chain)
			}
		case errors.Is(r.Err, ErrInsufficientFunds):
			failFunds++
		case errors.Is(r.Err, ErrFeeCapTooLow):
			failFeeCap++
		default:
			failOther++
			if failSample == nil {
				failSample = r.Err
			}
		}
	}

	// Collect receipts and logs, fixing cumulative gas.
	usedGas := uint64(0)
	var receipts block.Receipts
	var allLogs []*block.Log
	included := txs
	if failed > 0 {
		included = make([]*transaction.Transaction, 0, numTxs-failed)
	}
	// Nothing dropped: receipts, logs and the fee totals are assembled
	// across goroutines (P10: the serial loops were ~20 ms of a 163k
	// block's collect, mostly cache misses on every receipt).
	var feeTotals map[types.Address]*uint256.Int
	var feeOrder []types.Address
	collected := false
	if failed == 0 {
		if c, ok := collectParallelResults(txs, txResults, skipped, concreteHeader); ok {
			receipts, allLogs, usedGas, feeTotals, feeOrder = c.receipts, c.logs, c.usedGas, c.feeTotals, c.feeOrder
			collected = true
		}
	}
	for i := 0; i < numTxs && !collected; i++ {
		if skipped != nil && skipped[i] {
			// failed == 0 here (skip mode drops nothing), so the index is i.
			receipts = append(receipts, SkippedTxReceipt(txs[i], concreteHeader, i, usedGas))
			continue
		}
		if results[i].Err != nil {
			continue
		}
		tr := txResults[i]
		usedGas += tr.gasUsed
		if tr.receipt != nil {
			tr.receipt.CumulativeGasUsed = usedGas
			if failed > 0 {
				tr.receipt.TransactionIndex = uint(len(included))
				for _, l := range tr.receipt.Logs {
					l.TxIndex = uint(len(included))
					l.Index = uint(len(allLogs))
					allLogs = append(allLogs, l)
				}
			}
			receipts = append(receipts, tr.receipt)
		}
		if failed == 0 {
			allLogs = append(allLogs, tr.logs...)
		}
		if failed > 0 {
			included = append(included, txs[i])
		}
	}

	// Apply MVS final state to the real IntraBlockState.
	// This is critical: the caller (evmRecord) expects ibs to contain all state
	// changes. writeBlockWithState later calls ibs.CommitBlock() to flush to DB.
	tApplyStart := time.Now()
	if pf := ibs.AccountPrefetch(); pf != nil {
		// The workers read every account the block touched from the same
		// base state the fold reads (the parent's post-state): the fold and
		// the block end take them from that cache instead of the store.
		pf.SetKnown(baseCache)
	}
	applySplit, err := applyMVSToIBSTimed(executor.MVS(), numTxs, ibs)
	if err != nil {
		return nil, fmt.Errorf("ProcessParallel: failed to apply MVS state: %w", err)
	}
	executor.Release()
	tApplied := time.Now()

	// Read the delta-credited recipients ahead of the block-end fold, across
	// goroutines with their own store readers (the workers' view), and seed
	// the state's prefetch layer so the fold's sorted, serial reads become
	// map hits. Skipped when the state has no layer (a caller that did not
	// wire one) -- the fold then reads the store as before.
	var prefetched int
	if pf := ibs.AccountPrefetch(); pf != nil {
		// The prefetch readers use the locked lookup source, whose contract
		// is the tree's readers lock for their lifetime (on the leader the
		// live tree is being written by the import of the previous block
		// while the next build runs; a read racing an append, an eviction or
		// a resize is a crash or a seeded "absent" account).
		var pspan *commitment.ReadSpan
		if useQMDB && !ahead {
			var err error
			if pspan, err = p.bc.qmdbRootComputer.BeginReadSpan(context.Background(), p.bc.ChainDB); err != nil {
				return nil, fmt.Errorf("ProcessParallel: prefetch read span: %w", err)
			}
		}
		n, err := p.prefetchPendingCredits(ibs, pf, pspan, useQMDB, ahead, mode, postLayers, basePF)
		if pspan != nil {
			pspan.End()
		}
		if err != nil {
			return nil, fmt.Errorf("ProcessParallel: prefetch pending credits: %w", err)
		}
		prefetched = n
	}
	tPrefetched := time.Now()

	// Credit the deferred fees once per recipient, in transaction order. A
	// zero total still goes through AddBalance: the serial path touches the
	// recipient in every transaction, and the touch decides whether an empty
	// account survives the block.
	totals, order := feeTotals, feeOrder
	if !collected {
		totals = make(map[types.Address]*uint256.Int, len(feeRecipients))
	}
	for i := 0; i < numTxs && !collected; i++ {
		for _, f := range txResults[i].fees {
			t, ok := totals[f.recipient]
			if !ok {
				t = new(uint256.Int)
				totals[f.recipient] = t
				order = append(order, f.recipient)
			}
			t.Add(t, &f.amount)
		}
	}
	for _, r := range order {
		ibs.AddBalance(r, totals[r])
	}

	// Strict: validate gas against the header, then block end and Finalize
	// (rewards, the state root). Lenient: the builder's assemble does those.
	var nopay map[types.Address]*uint256.Int
	if !lenient && !ahead {
		var err error
		if nopay, err = p.finishStrict(concreteHeader, ibs, txs, usedGas); err != nil {
			return nil, err
		}
	}
	var aheadPhases ProcessPhases
	if ahead {
		aheadPhases = ProcessPhases{
			Recover: tRecovered.Sub(tStart),
			Setup:   tRunStart.Sub(tRecovered),
			// Setup's parts: up to the affinity pass, the pass, the
			// block-start system calls.
			SetupBefore:     tBlockStart.Sub(tRecovered),
			SetupAffinity:   tAffinity.Sub(tBlockStart),
			SetupBlockStart: tExecutorMade.Sub(tAffinity),
			Exec:            tRunEnd.Sub(tRunStart),
			Collect:         time.Since(tRunEnd),
			Skipped:         nSkipped,
			// Collect's parts.
			CollectReceipts: tApplyStart.Sub(tRunEnd),
			CollectApply:    tApplied.Sub(tApplyStart),
			ApplyWalk:       applySplit.walk,
			ApplyAccounts:   applySplit.accounts,
			ApplyStorage:    applySplit.storage,
			CollectPrefetch: tPrefetched.Sub(tApplied),
			CollectFees:     time.Since(tPrefetched),
			SkipReasons:     skipReasons,
		}
	} else if !lenient {
		// Publish this block's breakdown for the "blockimport phases" line,
		// which otherwise has nothing to print for a parallel block.
		p.lastPhases.Store(&ProcessPhases{
			Recover:     tRecovered.Sub(tStart),
			Setup:       tRunStart.Sub(tRecovered),
			Exec:        tRunEnd.Sub(tRunStart),
			Collect:     tApplyStart.Sub(tRunEnd),
			Finalize:    time.Since(tApplyStart),
			Skipped:     nSkipped,
			SkipReasons: skipReasons,
		})
	}
	if basePF != nil {
		noteAheadBaseUsed(base, concreteHeader.Number.Uint64(), basePF)
	}
	if execs, aborts := executor.Stats(); executor.FellBack() || numTxs >= 1000 {
		execNs, valNs := executor.WaveTimes()
		baseHits, baseMisses := basePF.Stats()
		var baseTook, baseClaimWait, baseTreeWait time.Duration
		var baseAt uint64
		baseDepth := int64(-1)
		baseSkip, baseCut := "", false
		if base != nil {
			baseTook, baseClaimWait, baseTreeWait, baseAt, baseSkip, baseCut = base.took, base.claimWait, base.waited, base.at, base.skip, base.cut.Load()
			if basePF != nil {
				baseDepth = int64(base.depth)
			}
		}
		if lenient && failed > 0 {
			log.Info("parallel fill drops", "n", concreteHeader.Number.Uint64(), "failed", failed,
				"nonceLow", failNonceLow, "nonceHigh", failNonceHigh, "funds", failFunds, "feeCap", failFeeCap, "other", failOther, "sample", failSample)
		}
		log.Info("parallel block", "n", concreteHeader.Number.Uint64(), "lenient", lenient, "failed", failed, "skipped", nSkipped, "txs", numTxs, "waves", executor.Waves(), "executions", execs, "aborts", aborts, "fallback", executor.FellBack(),
			"recoverMs", tRecovered.Sub(tStart).Milliseconds(), "hintHits", senderHintHits, "hintFills", senderHintFills, "setupMs", tRunStart.Sub(tRecovered).Milliseconds(), "blockStartMs", tBlockStart.Sub(tRecovered).Milliseconds(), "executorMs", tExecutorMade.Sub(tBlockStart).Milliseconds(), "affinityMs", tAffinity.Sub(tBlockStart).Milliseconds(), "sysStartMs", tExecutorMade.Sub(tAffinity).Milliseconds(), "prepHit", prepHit, "prepWaitMs", prepWait.Milliseconds(),
			"basePrefill", basePF.Len(), "basePrefillHits", baseHits, "basePrefillMisses", baseMisses, "basePrefillMs", baseTook.Milliseconds(), "basePrefillAt", baseAt, "basePrefillDepth", baseDepth, "basePrefillWaitMs", baseClaimWait.Milliseconds(), "basePrefillTreeWaitMs", baseTreeWait.Milliseconds(), "basePrefillCut", baseCut, "basePrefillSkip", baseSkip,
			"runMs", tRunEnd.Sub(tRunStart).Milliseconds(),
			"execMs", execNs/1e6, "validateMs", valNs/1e6, "collectMs", tApplyStart.Sub(tRunEnd).Milliseconds(), "applyMs", tApplied.Sub(tApplyStart).Milliseconds(), "prefetched", prefetched, "prefetchMs", tPrefetched.Sub(tApplied).Milliseconds(), "finalizeMs", time.Since(tPrefetched).Milliseconds(),
			"waveReads", waveReads, "waveColdReads", waveColdReads, "waveColdMs", waveColdMs, "waveBusyMs", waveBusyMs,
			"wkWallMaxMs", wkStats.WallMaxMs, "wkWallMinMs", wkStats.WallMinMs, "wkWallMeanMs", wkStats.WallMeanMs, "wkQMax", wkStats.QMax, "wkQMin", wkStats.QMin, "wkSetupMs", wkStats.SetupMs, "wkMvsWrMs", wkStats.MvsWriteMs, "wkMvsDelMs", wkStats.MvsDeleteMs, "wkKeys", wkStats.Keys, "wkTopChain", wkStats.TopChain)
	}

	return &parallelRun{Included: included, Receipts: receipts, Logs: allLogs, UsedGas: usedGas, Failed: failed, Skipped: nSkipped, SkipReasons: skipReasons, Nopay: nopay, phases: aheadPhases}, nil
}

// parallelBaseReader is the base state reader of one goroutine of a
// parallel run over its own transaction tx: the plain tables, the QMDB tree
// when reads come from it, the run's post-state layers on top. A locked run
// holds the tree's reader lock for its span; an ahead run takes it per
// lookup and retries an evicted entry through a fresh transaction.
func (p *StateProcessor) parallelBaseReader(tx kv.Tx, useQMDB, ahead bool, mode commitment.QMDBReadMode, postLayers []*state.PostState, basePF *commitment.BasePrefill) state.StateReader {
	var base state.StateReader = state.NewPlainStateReader(tx)
	if useQMDB {
		var src *commitment.LookupSource
		if ahead {
			src = commitment.NewLookupSourceRefreshing(p.bc.qmdbRootComputer, tx, p.bc.ChainDB)
			if basePF != nil {
				src.SetPrefill(basePF)
			}
		} else {
			src = commitment.NewLookupSourceLocked(p.bc.qmdbRootComputer, tx)
		}
		base = commitment.NewQMDBStateReader(src, base, mode)
	}
	return state.LayerPostStates(postLayers, base)
}

// waveWorkNs sums, over every parallel run of the process, the wall time
// the workers spent inside transaction bodies and their store writes
// (benchmarks: the exec wave's work without the scheduling gaps).
var waveWorkNs atomic.Int64

// waveCPUMeter, when set (benchmarks), reads the process CPU; every
// parallel run then adds its executor run's CPU to waveCPUNs.
var (
	waveCPUMeter func() time.Duration
	waveCPUNs    atomic.Int64
)

// preparedTx is a transaction's hash, built before the wave, and its
// transfer plan (first: a direct transfer reads the plan and the hash,
// adjacent, and not the message). Pointer-free: its message, when kept,
// is in the preparedSet's msgs.
type preparedTx struct {
	plan transferPlan
	hash types.Hash
	ok   bool
	// msgKept: the preparation kept the message (preparedSet.keptMsg).
	msgKept bool
}

// parallelTxScratch is a Block-STM worker's reusable per-transaction state
// for parallelApplyTx.
type parallelTxScratch struct {
	msg  transaction.Message
	fast fastTransferScratch
	// receipts is the unused tail of the worker's current receipt chunk:
	// one allocation per chunk instead of one per transaction. A chunk
	// stays reachable while any of its receipts is, and all of them belong
	// to the same block.
	receipts []block.Receipt
	chunk    int
	// msgReady says msg already holds this transaction's message (built
	// for a declined direct transfer); parallelApplyTx then skips building
	// it again.
	msgReady bool
}

// newReceipt returns a zero receipt carved from the scratch's chunk. The
// chunks grow from 16 to 256 receipts, so a small block does not pay for
// a large one's chunk.
func (sc *parallelTxScratch) newReceipt() *block.Receipt {
	if len(sc.receipts) == 0 {
		switch {
		case sc.chunk == 0:
			sc.chunk = 16
		case sc.chunk < 256:
			sc.chunk *= 2
		}
		sc.receipts = make([]block.Receipt, sc.chunk)
	}
	r := &sc.receipts[0]
	sc.receipts = sc.receipts[1:]
	return r
}

// fillReceipt builds a transaction's receipt from its execution result,
// carved from the scratch's chunk unless owned (a scratch made for one
// call).
func (sc *parallelTxScratch) fillReceipt(owned bool, tx *transaction.Transaction, txHash types.Hash, result *ExecutionResult, msg *transaction.Message, logs []*block.Log, header *block.Header, txIndex uint, evm vm2.VMInterface) *block.Receipt {
	var receipt *block.Receipt
	if owned {
		receipt = new(block.Receipt)
	} else {
		receipt = sc.newReceipt()
	}
	receipt.Type = tx.Type()
	if result.Failed() {
		receipt.Status = block.ReceiptStatusFailed
	} else {
		receipt.Status = block.ReceiptStatusSuccessful
	}
	receipt.TxHash = txHash
	receipt.GasUsed = result.UsedGas
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
	}
	receipt.Logs = logs
	if len(logs) != 0 {
		receipt.SetBloom(block.CreateBloom(block.Receipts{receipt}))
	} // else the zero bloom, which is what CreateBloom returns for no logs
	receipt.BlockNumber = header.Number
	receipt.TransactionIndex = txIndex
	return receipt
}

// transferReceipt is fillReceipt for a successful direct transfer (no
// logs, not a creation) of type txType.
func (sc *parallelTxScratch) transferReceipt(txType uint8, txHash types.Hash, result *ExecutionResult, header *block.Header, txIndex uint) *block.Receipt {
	receipt := sc.newReceipt()
	receipt.Type = txType
	receipt.Status = block.ReceiptStatusSuccessful
	receipt.TxHash = txHash
	receipt.GasUsed = result.UsedGas
	receipt.BlockNumber = header.Number
	receipt.TransactionIndex = txIndex
	return receipt
}

// parallelApplyTx executes a single transaction in the parallel context.
// Similar to applyTransaction but adapted for parallel execution with
// per-tx IntraBlockState and ParallelStateWriter. sc, when not nil, is the
// worker's reusable per-transaction scratch (its contents are overwritten).
func parallelApplyTx(
	config *params.ChainConfig,
	engine consensus.Engine,
	gp *common.GasPool,
	ibs *state.IntraBlockState,
	stateWriter state.StateWriter,
	header *block.Header,
	tx *transaction.Transaction,
	evm vm2.VMInterface,
	cfg vm2.Config,
	sink FeeSink,
	signer transaction.Signer,
	sc *parallelTxScratch,
) (*block.Receipt, uint64, []*block.Log, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, 0, nil, err
	}
	rules := evm.ChainRules()

	// The block's signer, built once per block by the caller (it re-derived
	// the fork rules for every transaction: 0.49 s of a follower's 25 s
	// profile). Nil falls back to building it here.
	if signer == nil {
		signer = transaction.MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time)
	}
	owned := sc == nil
	if owned {
		sc = new(parallelTxScratch)
		defer sc.fast.flush()
	}
	msg := &sc.msg
	if sc.msgReady {
		// The caller built it for the direct transfer attempt.
		sc.msgReady = false
	} else {
		if err := tx.AsMessageInto(msg, signer, header.BaseFee); err != nil {
			return nil, 0, nil, err
		}
		NormalizeExecutionMessage(msg, cfg.StatelessExec, engine != nil)
	}
	txHash := tx.Hash()

	// A plain EOA-to-EOA transfer skips the EVM (transfer_fast_path.go); any
	// other transaction, or one the fast path declines before writing
	// anything, runs the EVM path below in this same incarnation.
	var result *ExecutionResult
	fast := false
	if transferFastPath {
		if e, ok := evm.(*vm2.EVM); ok {
			result, fast = tryFastTransfer(e, ibs, msg, gp, sink, &sc.fast)
		}
	}
	if !fast {
		txContext := NewEVMTxContext(msg)
		evm.Reset(txContext, ibs)

		result, err = ApplyMessageWithFeeSink(evm, msg, gp, true, false, sink)
		if err != nil {
			return nil, 0, nil, err
		}
	}

	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, 0, nil, err
	}

	// Receipt bills the sender (post-refund); the block accumulates
	// EIP-7778 BlockGasUsed (pre-refund, floor-clamped) under Glamsterdam
	// so the parallel path validates against the same header.GasUsed the
	// serial miner/importer produce.
	blockGasUsed := result.BlockGasUsed
	logs := ibs.GetLogs(txHash)

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = sc.fillReceipt(owned, tx, txHash, result, msg, logs, header, uint(ibs.TxIndex()), evm)
	}

	return receipt, blockGasUsed, logs, nil
}

// applyMVSToIBS replays the final validated MVS state to the real IntraBlockState.
// Uses a two-pass approach:
//  1. Process FieldBalance entries to establish account existence/deletion.
//  2. Process FieldStorage and FieldCode for accounts that exist in the final state.
//
// This ordering ensures that storage/code changes are only applied to accounts
// that actually exist, preventing phantom account creation for deleted accounts.
func applyMVSToIBS(mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) error {
	_, err := applyMVSToIBSTimed(mvs, numTxs, ibs)
	return err
}

// applyMVSSplit is applyMVSToIBS's split: the final-versions walk, the
// account pass (wipes, accounts, credits), the storage and code passes.
type applyMVSSplit struct {
	walk, accounts, storage time.Duration
}

// applyMVSToIBSTimed is applyMVSToIBS, also returning its split.
func applyMVSToIBSTimed(mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) (split applyMVSSplit, err error) {
	t0 := time.Now()
	// Collect all MVS entries, separated by type.
	type accountEntry struct {
		addr  types.Address
		value []byte       // nil = deleted, unless delta is set
		delta *uint256.Int // set when the block only credited the account
	}
	type storageEntry struct {
		addr  types.Address
		slot  types.Hash
		value []byte
	}
	type codeEntry struct {
		addr types.Address
		code []byte
	}

	type collected struct {
		accounts []accountEntry
		storages []storageEntry
		codes    []codeEntry
		wipes    []types.Address
		err      error
	}
	collect := func(c *collected, lo, hi int) {
		c.err = mvs.ApplyShards(numTxs, lo, hi, func(key parallel.LocationKey, value []byte, delta *uint256.Int) error {
			switch key.Field {
			case parallel.FieldBalance:
				// FieldBalance stores the full encoded StateAccount (balance +
				// nonce + codeHash + root), or only a balance delta.
				c.accounts = append(c.accounts, accountEntry{addr: key.Address, value: value, delta: delta})
			case parallel.FieldStorage:
				c.storages = append(c.storages, storageEntry{addr: key.Address, slot: key.Slot, value: value})
			case parallel.FieldCode:
				if value != nil {
					c.codes = append(c.codes, codeEntry{addr: key.Address, code: value})
				}
			case parallel.FieldStorageWipe:
				if value != nil {
					c.wipes = append(c.wipes, key.Address)
				}
			}
			// Other fields (CodeHash, CodeSize, Suicide, Exist, Incarnation) are
			// auxiliary — the IBS handles them internally via SetBalance/SetCode/etc.
			return nil
		})
	}
	// The final versions are read across goroutines over disjoint shard
	// ranges (P10: the serial walk was ~11 ms of a 163k block's collect)
	// and concatenated in shard order, the order of the serial walk.
	parts := make([]collected, applyMVSWorkers(numTxs))
	if len(parts) == 1 {
		collect(&parts[0], 0, parallel.MVSShards)
	} else {
		var wg sync.WaitGroup
		per := (parallel.MVSShards + len(parts) - 1) / len(parts)
		for w := range parts {
			lo, hi := w*per, (w+1)*per
			if hi > parallel.MVSShards {
				hi = parallel.MVSShards
			}
			wg.Add(1)
			go func(c *collected, lo, hi int) {
				defer wg.Done()
				collect(c, lo, hi)
			}(&parts[w], lo, hi)
		}
		wg.Wait()
	}
	var accounts []accountEntry
	var storages []storageEntry
	var codes []codeEntry
	var wipes []types.Address
	if len(parts) == 1 {
		accounts, storages, codes, wipes = parts[0].accounts, parts[0].storages, parts[0].codes, parts[0].wipes
	} else {
		na, ns := 0, 0
		for i := range parts {
			na += len(parts[i].accounts)
			ns += len(parts[i].storages)
		}
		accounts = make([]accountEntry, 0, na)
		storages = make([]storageEntry, 0, ns)
		for i := range parts {
			accounts = append(accounts, parts[i].accounts...)
			storages = append(storages, parts[i].storages...)
			codes = append(codes, parts[i].codes...)
			wipes = append(wipes, parts[i].wipes...)
		}
	}
	for i := range parts {
		if parts[i].err != nil {
			return split, parts[i].err
		}
	}
	tWalk := time.Now()
	split.walk = tWalk.Sub(t0)

	full := 0
	for i := range accounts {
		if accounts[i].delta == nil {
			full++
		}
	}
	ibs.Reserve(full+len(storages)+len(codes), len(accounts)-full)

	// Track which accounts are deleted in the final state.
	deletedAccounts := make(map[types.Address]bool)
	for _, ae := range accounts {
		if ae.value == nil && ae.delta == nil {
			deletedAccounts[ae.addr] = true
		}
	}

	// Pass 0: storage wipes for addresses that are ALIVE in the final state
	// (recreate-after-SELFDESTRUCT / CREATE-on-existing). A plain SELFDESTRUCT
	// ends deleted and has its storage cleared by Selfdestruct in Pass 1, so
	// skip those here. CreateAccount(addr, true) preserves the balance and marks
	// the address in storageWipes, so CommitBlock clears the stale base slots
	// before the post-wipe slots from Pass 2 are written. Must run before Pass 1
	// (account) and Pass 2 (storage) so those values land on the recreated
	// object rather than being discarded by the wipe.
	for _, addr := range wipes {
		if deletedAccounts[addr] {
			continue
		}
		ibs.CreateAccount(addr, true)
	}

	// Pass 1: Apply account-level changes. A large block's entries go
	// through ApplyAccounts (P13c), which prepares the written accounts'
	// objects across goroutines and leaves the same serial sequence of
	// map inserts and journal records the loop below makes; a decode
	// error falls back to the loop, which reports it where it always did.
	pass1 := accounts
	if w := applyMVSWorkers(numTxs); applyBulkEnabled && w > 1 && len(accounts) >= applyBulkMinAccounts {
		if bulk, ok := decodeAccountApplies(len(accounts), w, func(i int) (types.Address, []byte, *uint256.Int) {
			return accounts[i].addr, accounts[i].value, accounts[i].delta
		}); ok {
			ibs.ApplyAccounts(bulk, w)
			pass1 = nil
		}
	}
	for _, ae := range pass1 {
		if ae.delta != nil {
			// The block only credited the account: add onto the base value,
			// which is the same deferred increment the serial path takes.
			ibs.AddBalance(ae.addr, ae.delta)
			continue
		}
		if ae.value == nil {
			// Account was deleted (selfdestructed).
			// Selfdestruct handles accounts that exist in base state;
			// for accounts created and deleted within the same block
			// (not in base state), Selfdestruct is a safe no-op.
			ibs.Selfdestruct(ae.addr)
		} else {
			acc, err := parallel.DecodeAccount(ae.value)
			if err != nil {
				return split, fmt.Errorf("decode account %v: %w", ae.addr, err)
			}
			ibs.SetBalance(ae.addr, &acc.Balance)
			ibs.SetNonce(ae.addr, acc.Nonce)
			// incarnation removed from StateAccount
		}
	}

	tAccounts := time.Now()
	split.accounts = tAccounts.Sub(tWalk)

	// Pass 2: Apply storage changes (skip deleted accounts).
	for _, se := range storages {
		if deletedAccounts[se.addr] {
			continue // storage of deleted accounts is discarded
		}
		var val uint256.Int
		if len(se.value) > 0 {
			val.SetBytes(se.value)
		}
		slot := se.slot
		ibs.SetState(se.addr, &slot, val)
	}

	// Pass 3: Apply code changes (skip deleted accounts).
	for _, ce := range codes {
		if deletedAccounts[ce.addr] {
			continue
		}
		ibs.SetCode(ce.addr, ce.code)
	}
	split.storage = time.Since(tAccounts)

	return split, nil
}

// applyBulkEnabled gates applyMVSToIBS's bulk account pass (P13c);
// N42_APPLY_BULK=0 restores the per-entry loop.
var applyBulkEnabled = os.Getenv("N42_APPLY_BULK") != "0"

// applyBulkMinAccounts is the account-entry count from which the bulk
// pass is taken.
const applyBulkMinAccounts = 4096

// decodeAccountApplies turns n account entries (entry(i): address, encoded
// final account or nil for deleted, credit delta) into ApplyAccounts'
// form across workers goroutines. ok is false when an encoding does not
// decode.
func decodeAccountApplies(n, workers int, entry func(int) (types.Address, []byte, *uint256.Int)) ([]state.AccountApply, bool) {
	out := make([]state.AccountApply, n)
	bad := make([]bool, workers)
	per := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var acc account.StateAccount
			for i := lo; i < hi; i++ {
				addr, value, delta := entry(i)
				o := &out[i]
				o.Addr = addr
				switch {
				case delta != nil:
					o.Delta = delta
				case value == nil:
					o.Delete = true
				default:
					acc = account.StateAccount{}
					if err := acc.DecodeForStorageV2(value); err != nil {
						bad[w] = true
						return
					}
					o.Balance = acc.Balance
					o.Nonce = acc.Nonce
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, b := range bad {
		if b {
			return nil, false
		}
	}
	return out, true
}

// collectedRun is collectParallelResults' outcome.
type collectedRun struct {
	receipts  block.Receipts
	logs      []*block.Log
	usedGas   uint64
	feeTotals map[types.Address]*uint256.Int
	feeOrder  []types.Address
}

// collectParallelMinTxs is the block size from which the receipts and fee
// totals are assembled across goroutines.
const collectParallelMinTxs = 8192

// collectParallelResults assembles a run in which no transaction was
// dropped (skipped ones keep a skip receipt): receipts with cumulative gas,
// logs in order, the gas used, and the deferred fee totals per recipient
// in order of first appearance -- exactly what the serial loops produce.
// It works in two passes over contiguous chunks (sums, then placement after
// a prefix over the chunk sums). ok is false when the block is small or a
// kept transaction has no receipt; the caller then runs the serial loops.
func collectParallelResults(txs []*transaction.Transaction, txResults []parallelTxResult, skipped []bool, header *block.Header) (collectedRun, bool) {
	n := len(txs)
	if n < collectParallelMinTxs {
		return collectedRun{}, false
	}
	workers := n / collectParallelMinTxs
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		workers = 2
	}
	per := (n + workers - 1) / workers
	type chunk struct {
		gas    uint64
		nLogs  int
		noRcpt bool
		fees   []deferredFee // summed per recipient, first appearance order
		gasOff uint64
		logOff int
		lo, hi int
	}
	chunks := make([]chunk, workers)
	for w := range chunks {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo > hi {
			lo = hi
		}
		chunks[w].lo, chunks[w].hi = lo, hi
	}
	isSkipped := func(i int) bool { return skipped != nil && skipped[i] }
	run := func(fn func(c *chunk)) {
		var wg sync.WaitGroup
		for w := range chunks {
			wg.Add(1)
			go func(c *chunk) {
				defer wg.Done()
				fn(c)
			}(&chunks[w])
		}
		wg.Wait()
	}
	run(func(c *chunk) {
		for i := c.lo; i < c.hi; i++ {
			tr := &txResults[i]
			for _, f := range tr.fees {
				k := 0
				for k < len(c.fees) && c.fees[k].recipient != f.recipient {
					k++
				}
				if k == len(c.fees) {
					c.fees = append(c.fees, deferredFee{recipient: f.recipient})
				}
				c.fees[k].amount.Add(&c.fees[k].amount, &f.amount)
			}
			if isSkipped(i) {
				continue
			}
			if tr.receipt == nil {
				c.noRcpt = true
				return
			}
			c.gas += tr.gasUsed
			c.nLogs += len(tr.logs)
		}
	})
	var out collectedRun
	nLogs := 0
	for w := range chunks {
		if chunks[w].noRcpt {
			return collectedRun{}, false
		}
		chunks[w].gasOff, chunks[w].logOff = out.usedGas, nLogs
		out.usedGas += chunks[w].gas
		nLogs += chunks[w].nLogs
	}
	out.receipts = make(block.Receipts, n)
	if nLogs > 0 {
		out.logs = make([]*block.Log, nLogs)
	}
	run(func(c *chunk) {
		cum, li := c.gasOff, c.logOff
		for i := c.lo; i < c.hi; i++ {
			if isSkipped(i) {
				out.receipts[i] = SkippedTxReceipt(txs[i], header, i, cum)
				continue
			}
			tr := &txResults[i]
			cum += tr.gasUsed
			tr.receipt.CumulativeGasUsed = cum
			out.receipts[i] = tr.receipt
			li += copy(out.logs[li:], tr.logs)
		}
	})
	out.feeTotals = make(map[types.Address]*uint256.Int, 2)
	for w := range chunks {
		for k := range chunks[w].fees {
			f := &chunks[w].fees[k]
			t, ok := out.feeTotals[f.recipient]
			if !ok {
				t = new(uint256.Int)
				out.feeTotals[f.recipient] = t
				out.feeOrder = append(out.feeOrder, f.recipient)
			}
			t.Add(t, &f.amount)
		}
	}
	return out, true
}

// ParallelWorkers reports the configured Block-STM worker count (for logs).
func ParallelWorkers() int { return parallelWorkers() }

// applyMVSWorkers is how many goroutines read a block's final versions in
// applyMVSToIBS: one for small blocks, up to 16 (and GOMAXPROCS).
func applyMVSWorkers(numTxs int) int {
	w := numTxs / 4096
	if p := runtime.GOMAXPROCS(0); w > p {
		w = p
	}
	if w > 16 {
		w = 16
	}
	if w < 1 {
		w = 1
	}
	return w
}

// prefetchPendingCredits reads the state's pending balance-increase
// addresses across goroutines, each on its own read transaction through the
// same reader stack the workers used, and seeds the prefetch layer with the
// result. Small sets are not worth the transactions and read through the
// fold as before.
// span is the caller's QMDB read span (nil: no QMDB reads); the workers'
// transactions are opened under its permit.
func (p *StateProcessor) prefetchPendingCredits(ibs *state.IntraBlockState, pf *state.AccountPrefetch, span *commitment.ReadSpan, useQMDB, ahead bool, mode commitment.QMDBReadMode, postLayers []*state.PostState, basePF *commitment.BasePrefill) (int, error) {
	if pf.HasKnown() {
		// The run's base-read cache answers the fold (SetKnown): every
		// credited account was read by the transaction that credited it,
		// so the cache holds them all, and a miss reads through as before.
		return 0, nil
	}
	addrs := ibs.PendingBalanceIncreases()
	if len(addrs) < 256 {
		return 0, nil
	}
	workers := parallelWorkers()
	if workers > 16 {
		workers = 16
	}
	if workers > len(addrs)/64 {
		workers = len(addrs) / 64
	}
	if workers < 1 {
		workers = 1
	}
	results := make([]*account.StateAccount, len(addrs))
	errs := make([]error, workers)
	var wg sync.WaitGroup
	per := (len(addrs) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(addrs) {
			hi = len(addrs)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var tx kv.Tx
			var err error
			if span != nil {
				tx, err = span.BeginRo(context.Background())
			} else {
				tx, err = p.bc.ChainDB.BeginRo(context.Background())
			}
			if err != nil {
				errs[w] = fmt.Errorf("prefetch worker %d: open read transaction: %w", w, err)
				return
			}
			defer tx.Rollback()
			base := p.parallelBaseReader(tx, useQMDB, ahead, mode, postLayers, basePF)
			for i := lo; i < hi; i++ {
				a, err := base.ReadAccountData(addrs[i])
				if err != nil {
					errs[w] = fmt.Errorf("prefetch worker %d: %w", w, err)
					return
				}
				results[i] = a
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return 0, err
		}
	}
	accts := make(map[types.Address]*account.StateAccount, len(addrs))
	for i, addr := range addrs {
		accts[addr] = results[i]
	}
	pf.Seed(accts)
	return len(addrs), nil
}
