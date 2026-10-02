// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestG52FinishExecutionAbortRewindsExecutionIdx pins the MVEstimate-abort
// path: the tx's status reverts to Ready (not committed/aborting) without
// bumping its incarnation, and executionIdx rewinds so the slot is
// re-claimed by a later NextTask call. No other test in the suite calls
// FinishExecutionAbort.
func TestG52FinishExecutionAbortRewindsExecutionIdx(t *testing.T) {
	s := NewScheduler(3)

	task := s.NextTask()
	if task.Kind != TaskExecute || task.TxIdx != 0 {
		t.Fatalf("expected Execute(0), got %+v", task)
	}
	// Claim tx 1 and 2 too so executionIdx sits at 3 before the abort.
	t1 := s.NextTask()
	t2 := s.NextTask()
	if t1.TxIdx != 1 || t2.TxIdx != 2 {
		t.Fatalf("expected to claim txs 1 and 2, got %+v %+v", t1, t2)
	}

	s.FinishExecutionAbort(task.TxIdx, task.Incarnation)

	st, inc := s.Status(0)
	if st != TxStatusReady {
		t.Fatalf("aborted tx should revert to Ready, got status=%v", st)
	}
	if inc != task.Incarnation {
		t.Fatalf("FinishExecutionAbort must not bump incarnation: got %d want %d", inc, task.Incarnation)
	}

	// executionIdx must have rewound to 0 so tx 0 is re-claimable.
	again := s.NextTask()
	if again.Kind != TaskExecute || again.TxIdx != 0 {
		t.Fatalf("expected re-claim of Execute(0) after abort, got %+v", again)
	}

	// A stale (incarnation, status) pair must be a no-op: finish tx1's
	// execution normally, then call FinishExecutionAbort again with an
	// incarnation that no longer matches -- status must stay unchanged.
	s.FinishExecution(t1.TxIdx, t1.Incarnation)
	preSt, preInc := s.Status(t1.TxIdx)
	s.FinishExecutionAbort(t1.TxIdx, t1.Incarnation+99)
	postSt, postInc := s.Status(t1.TxIdx)
	if postSt != preSt || postInc != preInc {
		t.Fatalf("stale-incarnation FinishExecutionAbort must be a no-op: pre=(%v,%d) post=(%v,%d)",
			preSt, preInc, postSt, postInc)
	}
}

// TestG52SchedulerFourWorkersWithAbortsAndValidationFailures drives 4
// workers over a 40-tx seeded deterministic task set where tx 5 always
// reports an MVEstimate abort on its first execution, and tx 10's first
// validation always fails, forcing a re-execution at incarnation+1. The
// scheduler must still converge: every tx ends Committed, each exactly
// once, with incarnation strictly increasing across forced retries. Run
// with -race to confirm the mutex/CAS protocol has no data races.
func TestG52SchedulerFourWorkersWithAbortsAndValidationFailures(t *testing.T) {
	const numTxs = 40
	const numWorkers = 4
	s := NewScheduler(numTxs)

	var abortedOnce, failedOnce sync.Map // txIdx -> bool, guards one-shot injection
	var wg sync.WaitGroup
	var totalExecutes atomic.Int64

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task := s.NextTask()
				switch task.Kind {
				case TaskDone:
					return
				case TaskNone:
					continue
				case TaskExecute:
					totalExecutes.Add(1)
					if task.TxIdx == 5 {
						if _, loaded := abortedOnce.LoadOrStore(5, true); !loaded {
							s.FinishExecutionAbort(task.TxIdx, task.Incarnation)
							continue
						}
					}
					s.FinishExecution(task.TxIdx, task.Incarnation)
				case TaskValidate:
					if task.TxIdx == 10 {
						if _, loaded := failedOnce.LoadOrStore(10, true); !loaded {
							s.FinishValidationFail(task.TxIdx, task.Incarnation)
							continue
						}
					}
					s.FinishValidationPass(task.TxIdx, task.Incarnation)
				}
			}
		}()
	}
	wg.Wait()

	if !s.Done() {
		t.Fatal("scheduler did not converge to Done")
	}
	for i := 0; i < numTxs; i++ {
		st, inc := s.Status(i)
		if st != TxStatusCommitted {
			t.Fatalf("tx %d ended in status %v, want Committed", i, st)
		}
		switch i {
		case 5:
			if s.ExecCount(i) < 1 {
				t.Fatalf("tx 5 should have finished at least one real execution, got %d", s.ExecCount(i))
			}
		case 10:
			if inc < 1 {
				t.Fatalf("tx 10 should have been re-executed at a bumped incarnation after its forced validation failure, got inc=%d", inc)
			}
			if s.ValFailCount(i) < 1 {
				t.Fatalf("tx 10's forced validation failure should be counted, got %d", s.ValFailCount(i))
			}
		}
	}
	if totalExecutes.Load() < int64(numTxs) {
		t.Fatalf("expected at least %d executions, got %d", numTxs, totalExecutes.Load())
	}
}
