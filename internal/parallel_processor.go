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
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
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
}

// deferredFee is a block-producer credit a transaction diverted to its fee
// sink; ProcessParallel sums them and credits the state once per recipient.
type deferredFee struct {
	recipient types.Address
	amount    *uint256.Int
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
	Nopay    map[types.Address]*uint256.Int
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
	run, err := p.runParallel(concreteHeader, b.Hash(), txs, ibs, blockHashFunc, false)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return run.Receipts, run.Nopay, run.Logs, run.UsedGas, nil
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
	return p.runParallel(header, types.Hash{}, txs, ibs, blockHashFunc, true)
}

// runParallel is the shared core: sender gate, per-worker readers, the
// executor, the fold into ibs, the deferred fee credit; strict mode then
// validates gas against the header and finalizes.
func (p *StateProcessor) runParallel(concreteHeader *block.Header, blockHash types.Hash, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash, lenient bool) (*parallelRun, error) {
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
		senderHintFills = applySenderHints(hints, signer, txs)
		// A block off the wire carries RLP only: From() is nil until the
		// signature is recovered. The affinity key below needs every sender
		// before the first wave, so recover the rest here in parallel
		// (memoised on the transaction; AsMessage reuses it). Round 35d/t:
		// with From() nil the key fell back to the index, a sender's nonce
		// chain ran on 32 workers at once, and every link but the first
		// failed its nonce check wave after wave until the 64-wave limit.
		recoverBlockSenders(signer, txs)
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
	txResults := make([]parallelTxResult, numTxs)

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
	}
	// A chained speculative build reads its unwritten own parents through
	// post-state snapshots layered on the block's reader; the per-worker
	// readers below open their own store transactions and must carry the
	// same layers, or the workers execute against a store that lacks the
	// parent (round 35zu: the faucet's funding block lost the parent's
	// reward and every follower rejected it on the state root). Followers
	// and unchained builds have no layers; this is a no-op for them.
	postLayers, _ := state.PostStateLayers(ibs.GetStateReader())
	var executor *parallel.Executor
	setup := func(workerID int) (any, func(), error) {
		tx, err := p.bc.ChainDB.BeginRo(context.Background())
		if err != nil {
			return nil, nil, fmt.Errorf("parallel worker %d: open read transaction: %w", workerID, err)
		}
		var base state.StateReader = state.NewPlainStateReader(tx)
		if useQMDB {
			base = commitment.NewQMDBStateReader(commitment.NewLookupSourceLocked(p.bc.qmdbRootComputer, tx), base, mode)
		}
		base = state.LayerPostStates(postLayers, base)
		wc := &workerCtx{tx: tx, base: base, observed: make(map[types.Address]struct{}, 8)}
		wc.reader = parallel.NewParallelStateReader(base, executor.MVS(), nil, 0)
		wc.ibs = state.New(wc.reader)
		wc.writer = parallel.NewParallelStateWriter(nil)
		// Every explicit balance read (GetBalance, Empty) of a transaction
		// is recorded: an account it credits without ever observing gets a
		// delta write, and its read of that account (for code, nonce) is
		// validated on every field but the balance.
		wc.ibs.SetBalanceReadHook(func(a types.Address) { wc.observed[a] = struct{}{} })
		wc.writer.SetDeltaEligible(func(a types.Address) bool { _, seen := wc.observed[a]; return !seen })
		wc.evm = vm2.NewEVM(blockContext, evmtypes.TxContext{}, wc.ibs, chainConfig, cfg)
		return wc, tx.Rollback, nil
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
		wc.ibs.Reset()
		clear(wc.observed)
		txIBS, pWriter := wc.ibs, wc.writer

		txIBS.Prepare(tx.Hash(), blockHash, txIndex)

		// Each tx gets its own gas pool (block-level gas validation happens after).
		gp := new(common.GasPool)
		gp.AddGas(concreteHeader.GasLimit)

		vmenv := wc.evm

		var fees []deferredFee
		sink := func(recipient types.Address, amount *uint256.Int) {
			fees = append(fees, deferredFee{recipient: recipient, amount: amount.Clone()})
		}
		receipt, gasUsed, logs, err := parallelApplyTx(chainConfig, p.engine, gp, txIBS, pWriter, concreteHeader, tx, vmenv, cfg, sink)
		rw.MarkBalanceInsensitive(func(a types.Address) bool { _, seen := wc.observed[a]; return seen })

		txResults[txIndex] = parallelTxResult{
			receipt: receipt,
			gasUsed: gasUsed,
			err:     err,
			logs:    logs,
			fees:    fees,
		}

		return err
	})

	// Pin each sender's transactions to one worker, in index order: the
	// nonce chain of a sender is then executed by the worker that already
	// applied its predecessor, and only cross-sender conflicts (a recipient
	// credited by several senders in one wave) reach the validator.
	executor.SetAffinity(func(txIndex int) uint64 {
		tx := txs[txIndex]
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
	tRunStart := time.Now()
	var unlockReaders func()
	if useQMDB {
		unlockReaders = p.bc.qmdbRootComputer.LockReaders()
	}
	results := executor.Run()
	if unlockReaders != nil {
		unlockReaders()
	}
	tRunEnd := time.Now()

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
	for i, r := range results {
		if r.Err == nil {
			continue
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
	tApplied := time.Now()

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
			t.Add(t, f.amount)
		}
	}
	for _, r := range order {
		ibs.AddBalance(r, totals[r])
	}

	// Strict: validate gas against the header, then block end and Finalize
	// (rewards, the state root). Lenient: the builder's assemble does those.
	var nopay map[types.Address]*uint256.Int
	if !lenient {
		if usedGas != concreteHeader.GasUsed {
			return nil, fmt.Errorf("gas used by execution: %d, in header: %d", usedGas, concreteHeader.GasUsed)
		}
		if _, err := ProcessExecutionBlockEnd(nil, p.config, ibs, concreteHeader, p.engine); err != nil {
			return nil, err
		}
		var err error
		_, nopay, err = p.engine.Finalize(p.bc, concreteHeader, ibs, txs, nil)
		if err != nil {
			return nil, err
		}
	}
	if execs, aborts := executor.Stats(); executor.FellBack() || numTxs >= 1000 {
		execNs, valNs := executor.WaveTimes()
		if lenient && failed > 0 {
			log.Info("parallel fill drops", "n", concreteHeader.Number.Uint64(), "failed", failed,
				"nonceLow", failNonceLow, "nonceHigh", failNonceHigh, "funds", failFunds, "feeCap", failFeeCap, "other", failOther, "sample", failSample)
		}
		log.Info("parallel block", "n", concreteHeader.Number.Uint64(), "lenient", lenient, "failed", failed, "txs", numTxs, "waves", executor.Waves(), "executions", execs, "aborts", aborts, "fallback", executor.FellBack(),
			"recoverMs", tRecovered.Sub(tStart).Milliseconds(), "hintHits", senderHintHits, "hintFills", senderHintFills, "setupMs", tRunStart.Sub(tRecovered).Milliseconds(), "runMs", tRunEnd.Sub(tRunStart).Milliseconds(),
			"execMs", execNs/1e6, "validateMs", valNs/1e6, "collectMs", tApplyStart.Sub(tRunEnd).Milliseconds(), "applyMs", tApplied.Sub(tApplyStart).Milliseconds(), "finalizeMs", time.Since(tApplied).Milliseconds())
	}

	return &parallelRun{Included: included, Receipts: receipts, Logs: allLogs, UsedGas: usedGas, Failed: failed, Nopay: nopay}, nil
}

// parallelApplyTx executes a single transaction in the parallel context.
// Similar to applyTransaction but adapted for parallel execution with
// per-tx IntraBlockState and ParallelStateWriter.
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
) (*block.Receipt, uint64, []*block.Log, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, 0, nil, err
	}
	rules := evm.ChainRules()

	msg, err := tx.AsMessage(transaction.MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time), header.BaseFee)
	if err != nil {
		return nil, 0, nil, err
	}
	NormalizeExecutionMessage(&msg, cfg.StatelessExec, engine != nil)

	txContext := NewEVMTxContext(&msg)
	evm.Reset(txContext, ibs)

	result, err := ApplyMessageWithFeeSink(evm, &msg, gp, true, false, sink)
	if err != nil {
		return nil, 0, nil, err
	}

	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, 0, nil, err
	}

	// Receipt bills the sender (post-refund); the block accumulates
	// EIP-7778 BlockGasUsed (pre-refund, floor-clamped) under Glamsterdam
	// so the parallel path validates against the same header.GasUsed the
	// serial miner/importer produce.
	gasUsed := result.UsedGas
	blockGasUsed := result.BlockGasUsed
	logs := ibs.GetLogs(tx.Hash())

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = &block.Receipt{Type: tx.Type()}
		if result.Failed() {
			receipt.Status = block.ReceiptStatusFailed
		} else {
			receipt.Status = block.ReceiptStatusSuccessful
		}
		receipt.TxHash = tx.Hash()
		receipt.GasUsed = gasUsed
		if msg.To() == nil {
			receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
		}
		receipt.Logs = logs
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = uint(ibs.TxIndex())
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
