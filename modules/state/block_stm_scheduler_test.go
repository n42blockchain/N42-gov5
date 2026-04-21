// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_SingleWorkerSequential(t *testing.T) {
	// One worker, 10 txs, no conflicts — should produce 10 Execute
	// then 10 Validate then TaskDone.
	s := NewScheduler(10)
	var executes, validates int
	for {
		task := s.NextTask()
		if task.Kind == TaskDone {
			break
		}
		if task.Kind == TaskNone {
			continue
		}
		switch task.Kind {
		case TaskExecute:
			executes++
			s.FinishExecution(task.TxIdx, task.Incarnation)
		case TaskValidate:
			validates++
			s.FinishValidationPass(task.TxIdx, task.Incarnation)
		}
	}
	if executes != 10 || validates != 10 {
		t.Errorf("executes=%d validates=%d; want 10,10", executes, validates)
	}
	if !s.Done() {
		t.Error("scheduler should be Done")
	}
	for i := 0; i < 10; i++ {
		st, _ := s.Status(i)
		if st != TxStatusCommitted {
			t.Errorf("tx %d status=%d want Committed", i, st)
		}
	}
}

func TestScheduler_MultiWorker(t *testing.T) {
	const numTxs = 50
	const numWorkers = 8
	s := NewScheduler(numTxs)
	var executes, validates atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task := s.NextTask()
				if task.Kind == TaskDone {
					return
				}
				if task.Kind == TaskNone {
					// Brief backoff.
					time.Sleep(time.Microsecond)
					continue
				}
				switch task.Kind {
				case TaskExecute:
					executes.Add(1)
					s.FinishExecution(task.TxIdx, task.Incarnation)
				case TaskValidate:
					validates.Add(1)
					s.FinishValidationPass(task.TxIdx, task.Incarnation)
				}
			}
		}()
	}
	wg.Wait()

	if executes.Load() != int64(numTxs) {
		t.Errorf("executes=%d want %d", executes.Load(), numTxs)
	}
	if validates.Load() < int64(numTxs) {
		// With backoff some validations may re-queue due to raciness,
		// but we expect at least numTxs passes.
		t.Errorf("validates=%d want >=%d", validates.Load(), numTxs)
	}
}

func TestScheduler_ValidationFailTriggersReexec(t *testing.T) {
	// Simulate: tx 3 fails validation once. Scheduler should bump its
	// incarnation, re-schedule execution from 3, and produce one more
	// Execute(3).
	const numTxs = 10
	s := NewScheduler(numTxs)
	failOnce := sync.Once{}
	executes := make(map[int]int) // txIdx -> count
	var validates int
	for {
		task := s.NextTask()
		if task.Kind == TaskDone {
			break
		}
		if task.Kind == TaskNone {
			continue
		}
		switch task.Kind {
		case TaskExecute:
			executes[task.TxIdx]++
			s.FinishExecution(task.TxIdx, task.Incarnation)
		case TaskValidate:
			validates++
			if task.TxIdx == 3 && task.Incarnation == 0 {
				// Fail first validation of tx 3.
				failed := false
				failOnce.Do(func() {
					s.FinishValidationFail(task.TxIdx, task.Incarnation)
					failed = true
				})
				if failed {
					continue
				}
			}
			s.FinishValidationPass(task.TxIdx, task.Incarnation)
		}
	}
	// tx 3 should have executed at least twice.
	if executes[3] < 2 {
		t.Errorf("tx 3 executes=%d want >=2", executes[3])
	}
	// Other txs may have re-executed (because of the rewind). Just
	// verify we finished.
	if !s.Done() {
		t.Error("not Done")
	}
	st, inc := s.Status(3)
	if st != TxStatusCommitted {
		t.Errorf("tx 3 status=%d want Committed", st)
	}
	if inc == 0 {
		t.Error("tx 3 incarnation should be >0 after rejection")
	}
}

func TestScheduler_StaleFailIgnored(t *testing.T) {
	// A validation fail delivered for a STALE incarnation (e.g. tx
	// has re-executed to inc=2 but a late worker still reports inc=0)
	// must be a no-op. Uses tx 2 committed at inc=0, then we manually
	// bump its incarnation to simulate a re-execution already underway,
	// then deliver FinishValidationFail(2, 0) which should be stale.
	s := NewScheduler(5)
	for {
		task := s.NextTask()
		if task.Kind == TaskDone {
			break
		}
		if task.Kind == TaskNone {
			continue
		}
		switch task.Kind {
		case TaskExecute:
			s.FinishExecution(task.TxIdx, task.Incarnation)
		case TaskValidate:
			s.FinishValidationPass(task.TxIdx, task.Incarnation)
		}
	}
	// Manually advance tx 2 incarnation to simulate an in-flight
	// re-execution (triggered by a DIFFERENT failure).
	s.txMu[2].Lock()
	s.incarnation[2] = 5
	s.txMu[2].Unlock()
	// Now deliver a fail for STALE inc=0. Must be ignored.
	s.FinishValidationFail(2, 0)
	st, inc := s.Status(2)
	if inc != 5 || st != TxStatusCommitted {
		t.Errorf("tx 2 post-stale-fail: status=%d inc=%d want Committed,5", st, inc)
	}
}

func TestScheduler_EmptyBlock(t *testing.T) {
	s := NewScheduler(0)
	task := s.NextTask()
	if task.Kind != TaskDone {
		t.Errorf("empty block: got %+v want TaskDone", task)
	}
	if !s.Done() {
		t.Error("empty block should be Done immediately")
	}
}

func TestScheduler_ValidationPriority(t *testing.T) {
	// When both execution and validation are available, scheduler
	// should prefer validation (progress unlocks downstream work).
	s := NewScheduler(10)

	// Execute tx 0 so validation becomes available.
	t0 := s.NextTask()
	if t0.Kind != TaskExecute || t0.TxIdx != 0 {
		t.Fatalf("expected Execute(0), got %+v", t0)
	}
	s.FinishExecution(0, 0)

	// Next task should be Validate(0), not Execute(1).
	t1 := s.NextTask()
	if t1.Kind != TaskValidate || t1.TxIdx != 0 {
		t.Errorf("expected Validate(0), got %+v", t1)
	}
}
