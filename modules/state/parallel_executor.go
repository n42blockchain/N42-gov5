// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ParallelExecutor wires the Block-STM scheduler, MVHashMap, and a pool
// of worker goroutines into a complete parallel tx processing pipeline.
// This file is the Phase 2 PoC "driver" — it has NO dependency on
// EVM or IntraBlockState yet; txs are represented as a TxExecutor
// function (see below). Phase 3 replaces that function with a real
// EVM bridge.

package state

import (
	"errors"
	"runtime"
	"sort"
	"sync"
	"time"
)

// TxExecutor is the per-tx user-provided function that reads/writes
// state via the MVStateView. It MUST be deterministic: given the same
// input state, it produces the same output writes (otherwise validation
// would always fail and we'd re-execute forever).
//
// Returns an error if the tx execution fails for business reasons
// (e.g. out of gas, invalid signature). Such errors do NOT cause
// re-execution; they're final. The scheduler still validates and
// commits the tx's state (which may include writes made BEFORE the
// error — EVM semantics preserve partial effects unless REVERT).
//
// If execution observes an MVEstimate (view.AbortPending()), TxExecutor
// should NOT return an error; the executor code in this file detects
// AbortPending and re-schedules automatically.
type TxExecutor func(view *MVStateView) error

// Result captures the final outcome of one tx after all re-executions.
type Result struct {
	TxIdx int
	Err   error // non-nil if the tx failed (out of gas etc.)
}

// ExecuteBlockParallel runs `numTxs` transactions in parallel using
// `numWorkers` goroutines. `executor(i)` returns the TxExecutor for
// tx i. `base` is the fallback store for reads that miss MVHashMap.
//
// Returns when all txs have committed OR the caller's context signals
// via ctxCancelled (a simple bool channel for PoC; Phase 3 wires to
// context.Context).
//
// Commit phase: after all txs commit, the caller uses
// ResultWriter.CollectWrites() to get the final state deltas in
// (key, value, txIdx) tuples, sorted by key for deterministic MDBX
// application.
func ExecuteBlockParallel(
	numTxs int,
	numWorkers int,
	base MVBaseReader,
	executor func(txIdx int) TxExecutor,
) ([]Result, *MVHashMap, error) {
	// Adapt the shared-base API to the per-worker-factory API by wrapping
	// the shared base in a factory that always returns the same instance
	// with a no-op closer. This preserves existing callers (tests,
	// MapBaseReader users where goroutine-sharing is safe) verbatim.
	factory := func() (MVBaseReader, func()) {
		return base, func() {}
	}
	return ExecuteBlockParallelWithFactory(numTxs, numWorkers, factory, executor)
}

// ExecuteBlockParallelWithFactory is the per-worker-base variant of
// ExecuteBlockParallel. Each worker goroutine calls baseFactory once on
// startup to get its own MVBaseReader + closer. This is REQUIRED for
// MDBX-backed readers because the erigon mdbx-go binding is not
// goroutine-safe at the transaction level. Sharing a single RoTx across
// workers trips a cgo "Go pointer to unpinned Go pointer" panic.
//
// The factory contract:
//   - Each call MUST return an MVBaseReader whose underlying kv.Tx is
//     distinct from every other concurrently-living base returned from
//     this factory.
//   - The returned closer MUST release the underlying tx (typically via
//     tx.Rollback()) when invoked.
//   - Returning the same base from multiple calls is allowed only when
//     the reader itself is goroutine-safe (e.g. tests using
//     MapBaseReader).
func ExecuteBlockParallelWithFactory(
	numTxs int,
	numWorkers int,
	baseFactory MVBaseFactory,
	executor func(txIdx int) TxExecutor,
) ([]Result, *MVHashMap, error) {
	if numTxs == 0 {
		return nil, NewMVHashMap(1), nil
	}
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	sched := NewScheduler(numTxs)
	mv := NewMVHashMap(nextPow2(numWorkers * 8))

	// Per-tx state the workers build up: the view used for the latest
	// incarnation (so validation can see the read set), and the set
	// of keys that incarnation wrote (for cleanup on abort).
	txViews := make([]*MVStateView, numTxs)
	txWrittenKeys := make([][]string, numTxs)
	txResults := make([]Result, numTxs)
	var txMu []sync.Mutex = make([]sync.Mutex, numTxs)

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			// Each worker owns its MVBaseReader (and, for MDBX-backed
			// readers, the underlying RoTx). The closer is invoked when
			// the worker exits.
			workerBase, closer := baseFactory()
			defer closer()
			for {
				task := sched.NextTask()
				if task.Kind == TaskDone {
					return
				}
				if task.Kind == TaskNone {
					// Brief backoff; avoids hammering the CAS counters
					// when no work is immediately available.
					time.Sleep(50 * time.Microsecond)
					continue
				}
				switch task.Kind {
				case TaskExecute:
					runExecute(sched, mv, workerBase, executor, task.TxIdx, task.Incarnation,
						txViews, txWrittenKeys, txResults, txMu)
				case TaskValidate:
					runValidate(sched, task.TxIdx, task.Incarnation,
						txViews, txWrittenKeys, txMu)
				}
			}
		}()
	}

	wg.Wait()

	return txResults, mv, nil
}

func runExecute(
	sched *Scheduler,
	mv *MVHashMap,
	base MVBaseReader,
	executor func(int) TxExecutor,
	txIdx int, inc uint32,
	txViews []*MVStateView,
	txWrittenKeys [][]string,
	txResults []Result,
	txMu []sync.Mutex,
) {
	// If a previous incarnation wrote keys, mark them estimate so
	// concurrent readers abort (per Block-STM paper). Then drop the
	// old writes from MVHashMap (new incarnation will write fresh).
	txMu[txIdx].Lock()
	prevKeys := txWrittenKeys[txIdx]
	prevView := txViews[txIdx]
	txMu[txIdx].Unlock()
	if prevView != nil {
		prevView.MarkWritesEstimate(prevKeys)
	}

	view := NewMVStateView(mv, base, txIdx, inc)
	err := executor(txIdx)(view)

	// If execution hit an Estimate read, the writer we depend on is
	// still speculative. This incarnation's result is bogus — do NOT
	// commit writes, do NOT transition to Executed. Tell scheduler to
	// re-queue the task (status stays Ready, executionIdx rewinds).
	// writeSet is local to the view (never flushed), so nothing to
	// undo in MVHashMap. Brief backoff avoids a tight CPU spin when
	// the blocking writer is slow.
	if view.AbortPending() {
		sched.FinishExecutionAbort(txIdx, inc)
		time.Sleep(100 * time.Microsecond)
		return
	}

	// Flush writes to MVHashMap and record the view for validation.
	keys := view.FlushWrites()
	sort.Strings(keys)

	// If the new incarnation's write set has FEWER keys than the
	// previous incarnation, the keys that are no longer written
	// must be REMOVED from MVHashMap (the stale entry would be
	// observable by later readers).
	if prevKeys != nil {
		newKeyset := make(map[string]struct{}, len(keys))
		for _, k := range keys {
			newKeyset[k] = struct{}{}
		}
		for _, pk := range prevKeys {
			if _, keep := newKeyset[pk]; !keep {
				mv.Delete([]byte(pk), txIdx)
			}
		}
	}

	txMu[txIdx].Lock()
	txViews[txIdx] = view
	txWrittenKeys[txIdx] = keys
	txResults[txIdx] = Result{TxIdx: txIdx, Err: err}
	txMu[txIdx].Unlock()

	sched.FinishExecution(txIdx, inc)
}

func runValidate(
	sched *Scheduler,
	txIdx int, inc uint32,
	txViews []*MVStateView,
	txWrittenKeys [][]string,
	txMu []sync.Mutex,
) {
	txMu[txIdx].Lock()
	view := txViews[txIdx]
	txMu[txIdx].Unlock()
	if view == nil {
		// Race: validate dispatched before execute stored the view.
		// Report pass; the scheduler will catch inconsistency via
		// downstream txs' validation.
		sched.FinishValidationPass(txIdx, inc)
		return
	}
	if view.Validate() {
		sched.FinishValidationPass(txIdx, inc)
		return
	}
	sched.FinishValidationFail(txIdx, inc)
}

// nextPow2 returns the smallest power of 2 >= v, with a floor of 1.
func nextPow2(v int) int {
	if v <= 1 {
		return 1
	}
	n := 1
	for n < v {
		n <<= 1
	}
	return n
}

// ErrDependencyCycle is returned if the executor detects infinite
// re-execution (two txs with circular estimate reads). Currently a
// placeholder; the PoC's naive retry will spin in that scenario —
// Phase 3 adds a bounded retry counter.
var ErrDependencyCycle = errors.New("parallel executor: dependency cycle")
