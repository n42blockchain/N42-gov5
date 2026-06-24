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
	"fmt"
	"os"
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
			runWorker(sched, mv, base, executor, txViews, txWrittenKeys, txResults, txMu)
		}()
	}

	wg.Wait()

	if err := reconcileCommittedViews(numTxs, mv, base, executor, txViews, txWrittenKeys, txResults, txMu); err != nil {
		return txResults, mv, err
	}

	return txResults, mv, nil
}

// ExecuteBlockParallelF is ExecuteBlockParallel but with a per-worker base
// factory instead of a single shared base. Each worker calls `factory()` once
// to obtain its OWN MVBaseReader (and a closer run on worker exit). This is
// REQUIRED when the base wraps a non-goroutine-safe resource such as a single
// MDBX read transaction (sharing one RoTx across workers trips mdbx-go's cgo
// goroutine-safety check) — e.g. NewMVBaseFromStateReader(NewHashedStateReader(tx)).
// A pre-flight factory call fails fast (and avoids a worker deadlock) if no base
// can be created at all.
func ExecuteBlockParallelF(
	numTxs int,
	numWorkers int,
	factory MVBaseFactory,
	executor func(txIdx int) TxExecutor,
) ([]Result, *MVHashMap, error) {
	if numTxs == 0 {
		return nil, NewMVHashMap(1), nil
	}
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	// Pre-flight: confirm a base can be created before launching workers (else a
	// total factory failure would leave the scheduler with no progressing worker
	// and wg.Wait would block forever).
	if pb, pc := factory(); pb == nil {
		return nil, nil, errors.New("ExecuteBlockParallelF: base factory returned nil")
	} else if pc != nil {
		pc()
	}

	sched := NewScheduler(numTxs)
	mv := NewMVHashMap(nextPow2(numWorkers * 8))
	txViews := make([]*MVStateView, numTxs)
	txWrittenKeys := make([][]string, numTxs)
	txResults := make([]Result, numTxs)
	txMu := make([]sync.Mutex, numTxs)

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			base, closer := factory()
			if base == nil {
				return // reduced parallelism; the pre-flight guarantees ≥1 base
			}
			if closer != nil {
				defer closer()
			}
			runWorker(sched, mv, base, executor, txViews, txWrittenKeys, txResults, txMu)
		}()
	}

	wg.Wait()

	base, closer := factory()
	if base == nil {
		return txResults, mv, errors.New("ExecuteBlockParallelF: final base factory returned nil")
	}
	if closer != nil {
		defer closer()
	}
	if err := reconcileCommittedViews(numTxs, mv, base, executor, txViews, txWrittenKeys, txResults, txMu); err != nil {
		return txResults, mv, err
	}

	// Per-failing-tx diagnostics for convergence debugging: logs how many times
	// each failed tx executed (incarnations), how many times its validation
	// failed, and its final status/incarnation. Lets us tell apart "scheduler
	// never re-triggered" (execCount==1, valFails==0) from "kept re-executing
	// but always read stale" (execCount>>1).
	if os.Getenv("N42_PEVM_TRACE") != "" {
		fires, skips := sched.RewindStats()
		nFail := 0
		totalWrittenKeys := 0
		nTxsWithWrites := 0
		for i, r := range txResults {
			if r.Err != nil {
				nFail++
			}
			if k := len(txWrittenKeys[i]); k > 0 {
				totalWrittenKeys += k
				nTxsWithWrites++
			}
		}
		fmt.Fprintf(os.Stderr,
			"PEVM_TRACE_BLOCK nTx=%d nFail=%d rewindFires=%d rewindSkips=%d nTxsWithWrites=%d totalKeys=%d\n",
			len(txResults), nFail, fires, skips, nTxsWithWrites, totalWrittenKeys)
		for i, r := range txResults {
			if r.Err != nil {
				st, inc := sched.FinalStatus(i)
				fmt.Fprintf(os.Stderr,
					"PEVM_TRACE failed tx=%d execCount=%d valFails=%d status=%d inc=%d err=%v\n",
					i, sched.ExecCount(i), sched.ValFailCount(i), st, inc, r.Err)
			}
		}
	}

	return txResults, mv, nil
}

// reconcileCommittedViews is a conservative final convergence pass for the
// optimistic scheduler. It walks txs in order, validates each read set against
// the final MV state built so far, and re-executes stale txs serially with a new
// incarnation. This preserves correctness for high-contention read/modify/write
// workloads even if the cooperative scheduler reached Done before every
// downstream invalidation was observed.
func reconcileCommittedViews(
	numTxs int,
	mv *MVHashMap,
	base MVBaseReader,
	executor func(int) TxExecutor,
	txViews []*MVStateView,
	txWrittenKeys [][]string,
	txResults []Result,
	txMu []sync.Mutex,
) error {
	for pass := 0; pass <= numTxs; pass++ {
		changed := false
		for txIdx := 0; txIdx < numTxs; txIdx++ {
			txMu[txIdx].Lock()
			view := txViews[txIdx]
			txMu[txIdx].Unlock()
			if view != nil && view.Validate() {
				continue
			}
			if err := rerunTxFinal(txIdx, mv, base, executor, txViews, txWrittenKeys, txResults, txMu); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			return nil
		}
	}
	return ErrDependencyCycle
}

func rerunTxFinal(
	txIdx int,
	mv *MVHashMap,
	base MVBaseReader,
	executor func(int) TxExecutor,
	txViews []*MVStateView,
	txWrittenKeys [][]string,
	txResults []Result,
	txMu []sync.Mutex,
) error {
	txMu[txIdx].Lock()
	prevKeys := txWrittenKeys[txIdx]
	prevView := txViews[txIdx]
	txMu[txIdx].Unlock()

	inc := uint32(0)
	if prevView != nil {
		inc = prevView.incarnation + 1
	}
	view := NewMVStateView(mv, base, txIdx, inc)
	err := executor(txIdx)(view)
	if view.AbortPending() {
		return ErrDependencyCycle
	}

	keys := view.FlushWrites()
	sort.Strings(keys)
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
	return nil
}

// runWorker is the shared Block-STM worker loop: pull a task, execute or
// validate, until the scheduler reports all txs committed.
func runWorker(
	sched *Scheduler,
	mv *MVHashMap,
	base MVBaseReader,
	executor func(int) TxExecutor,
	txViews []*MVStateView,
	txWrittenKeys [][]string,
	txResults []Result,
	txMu []sync.Mutex,
) {
	for {
		task := sched.NextTask()
		if task.Kind == TaskDone {
			return
		}
		if task.Kind == TaskNone {
			// Brief backoff; avoids hammering the CAS counters when no work is
			// immediately available.
			time.Sleep(50 * time.Microsecond)
			continue
		}
		switch task.Kind {
		case TaskExecute:
			runExecute(sched, mv, base, executor, task.TxIdx, task.Incarnation,
				txViews, txWrittenKeys, txResults, txMu)
		case TaskValidate:
			runValidate(sched, task.TxIdx, task.Incarnation,
				txViews, txWrittenKeys, txMu)
		}
	}
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

	// Block-STM frontier rewind: if this incarnation wrote (or removed) any
	// keys, higher txs that may have speculatively read them must re-validate.
	// MUST be called BEFORE FinishExecution: FinishExecution decrements numActive,
	// and if validationIdx is already at numTxs and numCommitted==numTxs, another
	// worker's NextTask sees numActive==0 and sets done=true → all workers exit
	// before our rewind can take effect (the convergence bug surfaced in
	// PEVM_TRACE: failing txs had execCount=1, valFails=0 — scheduler terminated
	// before re-validation ran). Doing the rewind FIRST keeps numActive non-zero
	// through the rewind, blocking the spurious termination check.
	if len(keys) > 0 || len(prevKeys) > 0 {
		sched.RewindValidationIdx(txIdx + 1)
	}

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
