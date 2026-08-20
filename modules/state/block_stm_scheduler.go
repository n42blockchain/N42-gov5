// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Block-STM scheduler for parallel EVM execution.
//
// This file implements the lock-free cooperative scheduler described in
// the Aptos Block-STM paper (https://arxiv.org/abs/2203.06871). The
// scheduler produces two task streams — Execute(txIdx, incarnation) and
// Validate(txIdx, incarnation) — that N worker goroutines drain in any
// order. The paper's invariants:
//
//   1. execution_idx, validation_idx are monotonic atomic counters.
//      Both start at 0 and only increase.
//   2. A task Validate(i) is produced ONLY after Execute(i) has completed
//      at least once.
//   3. After tx_i finishes executing, the scheduler schedules validation
//      for txs [i, num_txs); any downstream reader whose read-set was
//      observed under a stale i must re-execute.
//   4. If Validate(i) fails, the scheduler atomically decreases
//      validation_idx to i (via a CAS-retry loop) so subsequent
//      Validate calls start over from the failing tx.
//
// This file is the PURE scheduler — no EVM, no state access. Workers
// interleave scheduler calls with their own execute/validate routines
// that interact with MVHashMap and IntraBlockState (wired in Phase 2).
//
// Simplifications vs Aptos paper:
//
//   - No dependency list / condvar-based blocking on estimates. Instead,
//     the worker returning MVEstimate from MVHashMap.Read aborts its
//     tx and re-schedules — simpler logic at a small cost in throughput
//     for high-conflict workloads (acceptable for ETH mainnet where
//     87% of tx pairs don't conflict).
//   - No "decrease_cnt" generation counter. Phase 1 accepts that a
//     Validate call for a task being re-scheduled under lock contention
//     may redundantly run; the validation logic must therefore be
//     idempotent (tx's read-set check is pure function of MVHashMap).
//
// Phase 2 may reintroduce the paper's full fidelity if benchmarks show
// the simplifications costing meaningful throughput.

package state

import (
	"sync"
	"sync/atomic"
)

// TxStatus captures where a tx is in its lifecycle. Used only for
// diagnostics and the "done" detection logic.
type TxStatus uint8

const (
	TxStatusReady     TxStatus = iota // not yet executed
	TxStatusExecuting                 // claimed by a worker, in progress
	TxStatusExecuted                  // wrote to MVHashMap, awaiting validation
	TxStatusAborting                  // validation failed; awaiting re-exec
	TxStatusCommitted                 // validation passed, final
)

// TaskKind discriminates scheduler task output.
type TaskKind uint8

const (
	TaskNone     TaskKind = iota // no task available (caller should retry)
	TaskExecute                  // re-run tx at the given incarnation
	TaskValidate                 // verify tx's read set against MVHashMap
	TaskDone                     // all txs committed; scheduler is done
)

// Task is what a worker receives from Scheduler.NextTask.
type Task struct {
	Kind        TaskKind
	TxIdx       int
	Incarnation uint32
}

// Scheduler is the Block-STM cooperative task producer.
type Scheduler struct {
	numTxs int

	// Atomic counters.
	executionIdx  atomic.Int64 // next tx to dispatch for execution
	validationIdx atomic.Int64 // next tx to dispatch for validation
	numActive     atomic.Int64 // active workers (execute + validate)
	numCommitted  atomic.Int64 // count of txs currently in TxStatusCommitted
	done          atomic.Bool

	// Per-tx state. status/incarnation are guarded by txMu for
	// transitions that require multiple fields to move atomically.
	txMu        []sync.Mutex
	status      []TxStatus
	incarnation []uint32

	// Per-tx diagnostics (N42_PEVM_TRACE): execCount = times finished executing,
	// valFailCount = times validation failed, rewindCount = times a lower tx's
	// write rewound the frontier past this tx. Used to localize convergence bugs.
	execCount    []atomic.Int32
	valFailCount []atomic.Int32

	// Block-level rewind diagnostics: every RewindValidationIdx call either FIRES
	// (cur > target → CAS down) or SKIPS (cur ≤ target → guard returns no-op).
	// If skips dominate, validationIdx is already behind the target when the
	// lower tx finishes — meaning higher txs aren't past their first validation
	// yet, and the rewind never has anything to pull back. In that case the
	// missing trigger is somewhere else (e.g., in-order validation racing with
	// the lower tx's MV write).
	rewindFires atomic.Int64
	rewindSkips atomic.Int64
}

// NewScheduler creates a scheduler for a block of numTxs transactions.
func NewScheduler(numTxs int) *Scheduler {
	s := &Scheduler{
		numTxs:       numTxs,
		txMu:         make([]sync.Mutex, numTxs),
		status:       make([]TxStatus, numTxs),
		incarnation:  make([]uint32, numTxs),
		execCount:    make([]atomic.Int32, numTxs),
		valFailCount: make([]atomic.Int32, numTxs),
	}
	// All txs start as Ready with incarnation 0.
	return s
}

// ExecCount returns how many times tx_i has finished executing (diagnostics).
func (s *Scheduler) ExecCount(txIdx int) int32 { return s.execCount[txIdx].Load() }

// ValFailCount returns how many times tx_i's validation has failed (diagnostics).
func (s *Scheduler) ValFailCount(txIdx int) int32 { return s.valFailCount[txIdx].Load() }

// FinalStatus returns the tx's current status + incarnation (diagnostics).
func (s *Scheduler) FinalStatus(txIdx int) (TxStatus, uint32) { return s.Status(txIdx) }

// NumTxs returns the block's tx count.
func (s *Scheduler) NumTxs() int { return s.numTxs }

// Done returns true if all txs have committed.
func (s *Scheduler) Done() bool { return s.done.Load() }

// NextTask returns a task a worker should perform. A returned TaskNone
// means no task is available RIGHT NOW; the worker should retry (or
// spin briefly) — this is the "backoff" path. A returned TaskDone means
// the block has finished processing and the worker should exit.
//
// Design: validation has priority over execution because validating
// earlier txs unlocks more parallelism; if there are pending validation
// tasks (validation_idx < execution_idx), prefer those.
//
// Liveness: when multiple workers race on the same CAS, losers retry
// WITHIN the same call (bounded by progress of the winning CAS). A
// worker only returns TaskNone when the counters genuinely show no
// available work — not due to transient CAS contention. This avoids
// starvation where all N workers repeatedly sleep-and-retry against
// each other.
func (s *Scheduler) NextTask() Task {
	if s.done.Load() {
		return Task{Kind: TaskDone}
	}
	numTxs := int64(s.numTxs)

	// Inner loop: keep trying until EITHER we claim a task, OR no work
	// is currently available. Validation claims must only happen AFTER
	// the corresponding execution has finished (status transitioned to
	// Executed); otherwise FinishValidationPass's status guard would
	// silently drop the commit, and the tx would be stuck forever with
	// no way to get validated again (validationIdx already advanced).
	for {
		vIdx := s.validationIdx.Load()
		eIdx := s.executionIdx.Load()

		// Validation priority: claim Validate(vIdx) if
		//   (a) vIdx < eIdx (execution of that slot has been claimed)
		//   (b) vIdx < numTxs
		//   (c) status[vIdx] is Executed OR Committed (execution has
		//       FINISHED at least once; already-committed txs may need
		//       re-validation if an earlier tx's rewind invalidated
		//       them — demotion Committed→Aborting happens inside
		//       FinishValidationFail).
		// If (c) is false the tx is still Executing or Aborting —
		// fall through to the execute path so we claim another pending
		// execution rather than spinning on a validation that isn't
		// ready yet.
		if vIdx < eIdx && vIdx < numTxs {
			st, _ := s.Status(int(vIdx))
			if st == TxStatusExecuted || st == TxStatusCommitted {
				if s.validationIdx.CompareAndSwap(vIdx, vIdx+1) {
					inc := s.getIncarnation(int(vIdx))
					s.numActive.Add(1)
					return Task{Kind: TaskValidate, TxIdx: int(vIdx), Incarnation: inc}
				}
				continue // CAS lost — re-read and try again.
			}
			// Executing / Aborting / Ready — fall through.
		}

		// Execution path: claim eIdx FIRST via CAS, then decide whether
		// to actually execute based on current status. CAS-first is
		// critical: if we read status before CAS, a concurrent
		// FinishValidationFail can flip a Committed tx to Aborting
		// after our read but before our CAS — we'd skip a tx that
		// genuinely needs re-execution. With CAS-first, any status
		// transition by FinishValidationFail either (a) happened
		// before our CAS — we see the new status — or (b) happens
		// after our CAS, which also rewinds eIdx past our advance,
		// so the next NextTask iteration observes the now-Aborting
		// tx at the rewound eIdx.
		//
		// Txs already Executed/Committed at the claimed eIdx don't
		// need re-execution (validation handles verifying them); we
		// drop the claim without starting work and loop.
		if eIdx < numTxs {
			if s.executionIdx.CompareAndSwap(eIdx, eIdx+1) {
				// Atomic read-and-transition under txMu. If we find
				//   Executed/Committed:   no work — another worker
				//                         already ran this tx, or
				//                         it got committed via a
				//                         prior incarnation.
				//   Executing:            another worker currently
				//                         holds this tx (possible
				//                         after eIdx rewind races
				//                         the prior claim's status
				//                         transition). Skip to avoid
				//                         double-execute.
				//   Ready/Aborting:       transition to Executing and
				//                         return the task.
				// Doing this all under txMu is the only way to prevent
				// two workers from ever running the same (txIdx, inc)
				// concurrently; the lock is cheap and the hot path is
				// exactly one load + one store.
				s.txMu[eIdx].Lock()
				st := s.status[eIdx]
				if st == TxStatusExecuted || st == TxStatusCommitted || st == TxStatusExecuting {
					s.txMu[eIdx].Unlock()
					continue
				}
				inc := s.incarnation[eIdx]
				s.status[eIdx] = TxStatusExecuting
				s.txMu[eIdx].Unlock()
				s.numActive.Add(1)
				return Task{Kind: TaskExecute, TxIdx: int(eIdx), Incarnation: inc}
			}
			continue // CAS lost — re-read.
		}

		// Both counters past end. Terminal condition: numActive=0 AND
		// numCommitted==numTxs. Otherwise a tx is still in flight or
		// awaiting validation post-rewind. A rewind racing with another
		// worker's counter claim can occasionally leave both frontiers at
		// the end even though a Ready/Aborting or Executed tx remains. When
		// the scheduler is quiescent, reconstruct the missing frontier from
		// the guarded per-tx statuses instead of spinning forever.
		if s.numActive.Load() == 0 {
			if s.numCommitted.Load() == numTxs {
				s.done.Store(true)
				return Task{Kind: TaskDone}
			}
			if s.recoverQuiescentWork() {
				continue
			}
		}
		return Task{Kind: TaskNone}
	}
}

// recoverQuiescentWork repairs a lost scheduler frontier after all published
// tasks have finished. Per-tx status is the source of truth: Ready/Aborting
// needs execution, while Executed needs validation. Committed txs need no new
// work. The final numActive check avoids acting on a snapshot taken while a
// concurrent worker was publishing a newly claimed task.
func (s *Scheduler) recoverQuiescentWork() bool {
	minExecution := s.numTxs
	minValidation := s.numTxs
	inFlight := false
	for txIdx := 0; txIdx < s.numTxs; txIdx++ {
		s.txMu[txIdx].Lock()
		status := s.status[txIdx]
		s.txMu[txIdx].Unlock()
		switch status {
		case TxStatusReady, TxStatusAborting:
			if txIdx < minExecution {
				minExecution = txIdx
			}
		case TxStatusExecuted:
			if txIdx < minValidation {
				minValidation = txIdx
			}
		case TxStatusExecuting:
			inFlight = true
		}
	}
	if s.numActive.Load() != 0 {
		return true
	}
	if minExecution < s.numTxs {
		rewindCounter(&s.executionIdx, int64(minExecution))
		return true
	}
	if minValidation < s.numTxs {
		rewindCounter(&s.validationIdx, int64(minValidation))
		return true
	}
	return inFlight
}

func rewindCounter(counter *atomic.Int64, target int64) {
	for {
		current := counter.Load()
		if current <= target || counter.CompareAndSwap(current, target) {
			return
		}
	}
}

// FinishExecution marks tx_i as having completed its Execute(inc) task.
// The scheduler makes it eligible for validation. Workers call this
// after writing to MVHashMap. Only transitions if we are still in
// Executing at the claimed incarnation; guards against stale callers
// demoting a tx that has already advanced (e.g. to Committed).
func (s *Scheduler) FinishExecution(txIdx int, inc uint32) {
	s.txMu[txIdx].Lock()
	if s.incarnation[txIdx] == inc && s.status[txIdx] == TxStatusExecuting {
		s.status[txIdx] = TxStatusExecuted
	}
	s.txMu[txIdx].Unlock()
	s.execCount[txIdx].Add(1)
	s.numActive.Add(-1)
}

// RewindValidationIdx decreases validationIdx down to `target` (no-op if it is
// already <= target). This is the canonical Block-STM decrease_validation_idx:
// the caller invokes it AFTER an execution flushes writes that higher txs may
// have speculatively read, forcing those higher txs (even already-Committed
// ones) to re-validate — and, on the resulting read-mismatch, re-execute.
//
// Without this, a higher tx that read a key from the base store BEFORE a lower
// tx wrote it would validate-pass (no writer existed at read time) and commit a
// STALE result — most visibly a same-sender "nonce too high" for a tx whose
// lower-nonce sibling hadn't executed yet. (The old code only rewound on
// validation FAILURE, never on execution, so the universal coinbase write was
// silently masking this — every read hit an MVEstimate. Lazy-coinbase removed
// that crutch and exposed the missing rewind.)
func (s *Scheduler) RewindValidationIdx(target int) {
	t := int64(target)
	for {
		cur := s.validationIdx.Load()
		if cur <= t {
			s.rewindSkips.Add(1)
			return
		}
		if s.validationIdx.CompareAndSwap(cur, t) {
			s.rewindFires.Add(1)
			return
		}
	}
}

// RewindStats returns (fires, skips) for diagnostics.
func (s *Scheduler) RewindStats() (int64, int64) {
	return s.rewindFires.Load(), s.rewindSkips.Load()
}

// FinishExecutionAbort is called when a worker's Execute task observed
// an MVEstimate read (the writer it depends on is still speculative /
// re-executing). The tx's partial writes are NOT flushed to MVHashMap;
// the tx must be re-scheduled to try again once the blocking writer
// settles. Incarnation is NOT bumped (same read set expected); the
// scheduler rewinds executionIdx so NextTask re-claims this slot.
//
// Distinguished from FinishValidationFail, which handles the
// Executed → Aborting transition triggered by a validation mismatch.
func (s *Scheduler) FinishExecutionAbort(txIdx int, inc uint32) {
	s.txMu[txIdx].Lock()
	if s.incarnation[txIdx] == inc && s.status[txIdx] == TxStatusExecuting {
		s.status[txIdx] = TxStatusReady
	}
	s.txMu[txIdx].Unlock()
	// Rewind executionIdx so this tx gets re-claimed by the next
	// worker. Best-effort CAS: another worker may have advanced past
	// us, in which case our rewind is a no-op.
	for {
		cur := s.executionIdx.Load()
		if cur <= int64(txIdx) {
			break
		}
		if s.executionIdx.CompareAndSwap(cur, int64(txIdx)) {
			break
		}
	}
	s.numActive.Add(-1)
}

// FinishValidationPass marks tx_i's Validate(inc) as passing. Nothing
// further is needed for this tx at this incarnation.
func (s *Scheduler) FinishValidationPass(txIdx int, inc uint32) {
	s.txMu[txIdx].Lock()
	committed := false
	if s.incarnation[txIdx] == inc && s.status[txIdx] == TxStatusExecuted {
		s.status[txIdx] = TxStatusCommitted
		committed = true
	}
	s.txMu[txIdx].Unlock()
	if committed {
		s.numCommitted.Add(1)
	}
	s.numActive.Add(-1)
}

// FinishValidationFail marks tx_i's Validate(inc) as failing. The
// scheduler bumps the incarnation, schedules re-execution, and rewinds
// validationIdx so subsequent validations re-check from txIdx onward.
func (s *Scheduler) FinishValidationFail(txIdx int, inc uint32) {
	s.valFailCount[txIdx].Add(1)
	s.txMu[txIdx].Lock()
	abort := s.incarnation[txIdx] == inc &&
		(s.status[txIdx] == TxStatusExecuted || s.status[txIdx] == TxStatusCommitted)
	wasCommitted := s.status[txIdx] == TxStatusCommitted
	if abort {
		s.incarnation[txIdx] = inc + 1
		s.status[txIdx] = TxStatusAborting
	}
	s.txMu[txIdx].Unlock()
	if wasCommitted && abort {
		s.numCommitted.Add(-1)
	}
	if !abort {
		// Already re-scheduled by a concurrent fail. Drop our claim.
		s.numActive.Add(-1)
		return
	}
	// Rewind executionIdx if it's past txIdx; this re-schedules
	// execution for tx_i. The rewind is a best-effort CAS: another
	// worker may have advanced executionIdx concurrently, in which
	// case we leave it (the scheduler will eventually re-queue).
	for {
		cur := s.executionIdx.Load()
		if cur <= int64(txIdx) {
			break
		}
		if s.executionIdx.CompareAndSwap(cur, int64(txIdx)) {
			break
		}
	}
	// Also rewind validationIdx — all txs from txIdx onward must
	// re-validate since they may have read from tx_i.
	for {
		cur := s.validationIdx.Load()
		if cur <= int64(txIdx) {
			break
		}
		if s.validationIdx.CompareAndSwap(cur, int64(txIdx)) {
			break
		}
	}
	s.numActive.Add(-1)
}

// beginExecution transitions tx_i from Ready/Aborting to Executing and
// returns the current incarnation for the caller to stamp on its writes.
// For safety, only transitions IF current status is Ready or Aborting;
// Executed/Committed stay as-is (shouldn't happen because NextTask
// filters, but we defend against the race).
func (s *Scheduler) beginExecution(txIdx int) uint32 {
	s.txMu[txIdx].Lock()
	inc := s.incarnation[txIdx]
	if s.status[txIdx] == TxStatusReady || s.status[txIdx] == TxStatusAborting {
		s.status[txIdx] = TxStatusExecuting
	}
	s.txMu[txIdx].Unlock()
	return inc
}

// getIncarnation returns the current incarnation of tx_i without
// changing its status.
func (s *Scheduler) getIncarnation(txIdx int) uint32 {
	s.txMu[txIdx].Lock()
	inc := s.incarnation[txIdx]
	s.txMu[txIdx].Unlock()
	return inc
}

// Status returns a snapshot of tx_i's current status. Intended for
// diagnostics; the returned value may be stale the instant it returns.
func (s *Scheduler) Status(txIdx int) (TxStatus, uint32) {
	s.txMu[txIdx].Lock()
	defer s.txMu[txIdx].Unlock()
	return s.status[txIdx], s.incarnation[txIdx]
}
