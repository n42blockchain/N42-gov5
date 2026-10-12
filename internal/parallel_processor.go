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
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
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
			r = r[:n]
			clear(r)
			return r
		}
	}
	return make([]parallelTxResult, n)
}

func giveTxResults(r []parallelTxResult) {
	clear(r) // drop the receipt and log pointers now, not at the next take
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
// run) and retry an entry evicted under them through a fresh transaction,
// so the parent's root and write proceed on the tree meanwhile. ibs reads
// the parent's unwritten post-state through its post-state layers.
// FinishParallel completes the block once its parent is written.
func (p *StateProcessor) ExecuteParallelAhead(b *block.Block, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (*parallelRun, error) {
	if !p.ParallelApplicable(b) {
		return nil, ErrParallelNotApplicable
	}
	hdr := b.Header().(*block.Header)
	body, _ := b.Body().(*block.Body)
	txs := b.Transactions()
	return p.runParallelMode(hdr, b.Hash(), txs, ibs, blockHashFunc, false, body.IsFrameBody(), true, FrameSpansOf(body, len(txs)))
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
	return p.runParallelMode(concreteHeader, blockHash, txs, ibs, blockHashFunc, lenient, skipInvalid, false, spans)
}

// runParallelMode is runParallel; ahead (strict runs only) stops after the
// fold, before block end and Finalize, and reads the QMDB tree without
// holding its reader lock across the run (ExecuteParallelAhead).
func (p *StateProcessor) runParallelMode(concreteHeader *block.Header, blockHash types.Hash, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash, lenient, skipInvalid, ahead bool, spans []FrameSpan) (*parallelRun, error) {
	numTxs := len(txs)
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
		hits, err := verifyBlockSendersHinted(signer, txs, hints)
		if err != nil {
			return nil, fmt.Errorf("block %s: %w", concreteHeader.Number.String(), err)
		}
		senderHintHits = hits
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
		p.bc.recoverBlockSendersFor(blockHash, signer, txs, spans)
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

	if err := ProcessExecutionBlockStart(concreteHeader.ParentBeaconRoot, chainConfig, ibs, concreteHeader, p.engine); err != nil {
		return nil, err
	}
	// blockContext is a value type — each goroutine's NewEVM copies it, safe to share.
	blockContext := NewEVMBlockContext(concreteHeader, blockHashFunc, p.engine, chainConfig, nil)

	// Per-tx result storage. Each goroutine writes to its own index (no race).
	tBlockStart := time.Now()
	txResults := takeTxResults(numTxs)
	defer giveTxResults(txResults)

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
	prepared := make([]preparedTx, numTxs)
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
		base := p.parallelBaseReader(tx, useQMDB, ahead, mode, postLayers)
		wc := &workerCtx{tx: tx, base: base, observed: make(map[types.Address]struct{}, 8)}
		wc.reader = parallel.NewParallelStateReader(base, executor.MVS(), nil, 0)
		// One base-read cache for the whole block, shared by every worker: the
		// base is the parent's post-state and cannot change while the block
		// executes, and each transaction otherwise pays its own read of the
		// accounts it touches (~163k reads for ~23k accounts at the bench's
		// block size).
		wc.reader.SetBaseCache(baseCache)
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
		if pm := &prepared[txIndex]; pm.ok {
			if transferFastPath && transferDirectPath {
				// A plain transfer straight on the reader and writer,
				// without the IntraBlockState (tryDirectTransfer).
				if res, ok := tryDirectTransfer(wc.evm, wc.reader, wc.writer, rw, &pm.msg, concreteHeader.GasLimit, wc.sink, &wc.scratch.fast); ok {
					var receipt *block.Receipt
					if !cfg.NoReceipts {
						receipt = wc.scratch.fillReceipt(false, tx, pm.hash, res, &pm.msg, nil, concreteHeader, uint(txIndex), wc.evm)
					}
					r.receipt, r.gasUsed, r.err, r.logs = receipt, res.BlockGasUsed, nil, nil
					r.setFees(wc.fees)
					return nil
				}
			}
			// The EVM path may adjust its message: it gets a copy.
			wc.scratch.msg = pm.msg
			wc.scratch.msgReady = true
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
		tx := txs[txIndex]
		if pm := &prepared[txIndex]; !pm.ok && signer != nil {
			if tx.AsMessageInto(&pm.msg, signer, concreteHeader.BaseFee) == nil {
				NormalizeExecutionMessage(&pm.msg, cfg.StatelessExec, p.engine != nil)
				pm.hash = tx.Hash()
				pm.ok = true
			}
		}
		if from := tx.From(); from != nil {
			return binary.LittleEndian.Uint64(from[:8])
		}
		if signer != nil {
			if from, err := transaction.Sender(signer, tx); err == nil {
				return binary.LittleEndian.Uint64(from[:8])
			}
		}
		return uint64(txIndex)
	})
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
	// this snapshot would double-count that caller's reads too.
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
	results := executor.Run()
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
	for i := 0; i < numTxs; i++ {
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
	if err := applyMVSToIBS(executor.MVS(), numTxs, ibs); err != nil {
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
		n, err := p.prefetchPendingCredits(ibs, pf, pspan, useQMDB, ahead, mode, postLayers)
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
	totals := make(map[types.Address]*uint256.Int, len(feeRecipients))
	var order []types.Address
	for i := 0; i < numTxs; i++ {
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
			Exec:    tRunEnd.Sub(tRunStart),
			Collect: time.Since(tRunEnd),
			Skipped: nSkipped,
			// Collect's parts.
			CollectReceipts: tApplyStart.Sub(tRunEnd),
			CollectApply:    tApplied.Sub(tApplyStart),
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
	if execs, aborts := executor.Stats(); executor.FellBack() || numTxs >= 1000 {
		execNs, valNs := executor.WaveTimes()
		if lenient && failed > 0 {
			log.Info("parallel fill drops", "n", concreteHeader.Number.Uint64(), "failed", failed,
				"nonceLow", failNonceLow, "nonceHigh", failNonceHigh, "funds", failFunds, "feeCap", failFeeCap, "other", failOther, "sample", failSample)
		}
		log.Info("parallel block", "n", concreteHeader.Number.Uint64(), "lenient", lenient, "failed", failed, "skipped", nSkipped, "txs", numTxs, "waves", executor.Waves(), "executions", execs, "aborts", aborts, "fallback", executor.FellBack(),
			"recoverMs", tRecovered.Sub(tStart).Milliseconds(), "hintHits", senderHintHits, "hintFills", senderHintFills, "setupMs", tRunStart.Sub(tRecovered).Milliseconds(), "blockStartMs", tBlockStart.Sub(tRecovered).Milliseconds(), "executorMs", tExecutorMade.Sub(tBlockStart).Milliseconds(), "runMs", tRunEnd.Sub(tRunStart).Milliseconds(),
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
func (p *StateProcessor) parallelBaseReader(tx kv.Tx, useQMDB, ahead bool, mode commitment.QMDBReadMode, postLayers []*state.PostState) state.StateReader {
	var base state.StateReader = state.NewPlainStateReader(tx)
	if useQMDB {
		var src *commitment.LookupSource
		if ahead {
			src = commitment.NewLookupSourceRefreshing(p.bc.qmdbRootComputer, tx, p.bc.ChainDB)
		} else {
			src = commitment.NewLookupSourceLocked(p.bc.qmdbRootComputer, tx)
		}
		base = commitment.NewQMDBStateReader(src, base, mode)
	}
	return state.LayerPostStates(postLayers, base)
}

// preparedTx is a transaction's message and hash, built before the wave.
type preparedTx struct {
	msg  transaction.Message
	hash types.Hash
	ok   bool
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
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
	} // else the zero bloom, which is what CreateBloom returns for no logs
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

	var accounts []accountEntry
	var storages []storageEntry
	var codes []codeEntry
	var wipes []types.Address

	// Single pass to collect and categorize all MVS entries.
	if err := mvs.ApplyAll(numTxs, func(key parallel.LocationKey, value []byte, delta *uint256.Int) error {
		switch key.Field {
		case parallel.FieldBalance:
			// FieldBalance stores the full encoded StateAccount (balance +
			// nonce + codeHash + root), or only a balance delta.
			accounts = append(accounts, accountEntry{addr: key.Address, value: value, delta: delta})
		case parallel.FieldStorage:
			storages = append(storages, storageEntry{addr: key.Address, slot: key.Slot, value: value})
		case parallel.FieldCode:
			if value != nil {
				codes = append(codes, codeEntry{addr: key.Address, code: value})
			}
		case parallel.FieldStorageWipe:
			if value != nil {
				wipes = append(wipes, key.Address)
			}
		}
		// Other fields (CodeHash, CodeSize, Suicide, Exist, Incarnation) are
		// auxiliary — the IBS handles them internally via SetBalance/SetCode/etc.
		return nil
	}); err != nil {
		return err
	}

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

	// Pass 1: Apply account-level changes.
	for _, ae := range accounts {
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
				return fmt.Errorf("decode account %v: %w", ae.addr, err)
			}
			ibs.SetBalance(ae.addr, &acc.Balance)
			ibs.SetNonce(ae.addr, acc.Nonce)
			// incarnation removed from StateAccount
		}
	}

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

	return nil
}

// ParallelWorkers reports the configured Block-STM worker count (for logs).
func ParallelWorkers() int { return parallelWorkers() }

// prefetchPendingCredits reads the state's pending balance-increase
// addresses across goroutines, each on its own read transaction through the
// same reader stack the workers used, and seeds the prefetch layer with the
// result. Small sets are not worth the transactions and read through the
// fold as before.
// span is the caller's QMDB read span (nil: no QMDB reads); the workers'
// transactions are opened under its permit.
func (p *StateProcessor) prefetchPendingCredits(ibs *state.IntraBlockState, pf *state.AccountPrefetch, span *commitment.ReadSpan, useQMDB, ahead bool, mode commitment.QMDBReadMode, postLayers []*state.PostState) (int, error) {
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
			base := p.parallelBaseReader(tx, useQMDB, ahead, mode, postLayers)
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
