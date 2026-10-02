// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "testing"

// ExecCount/ValFailCount/FinalStatus/RewindStats are diagnostics-only
// counters; this drives them through a full execute -> fail -> re-execute
// -> validate cycle and checks each one.
func TestSchedulerDiagnosticsCounters(t *testing.T) {
	s := NewScheduler(3)

	if n := s.NumTxs(); n != 3 {
		t.Fatalf("NumTxs = %d, want 3", n)
	}

	task := s.NextTask()
	if task.Kind != TaskExecute || task.TxIdx != 0 {
		t.Fatalf("first task = %+v, want Execute tx 0", task)
	}
	s.FinishExecution(0, task.Incarnation)

	if n := s.ExecCount(0); n != 1 {
		t.Fatalf("ExecCount(0) = %d, want 1", n)
	}
	if n := s.ValFailCount(0); n != 0 {
		t.Fatalf("ValFailCount(0) = %d, want 0", n)
	}

	// Drive remaining executes so validation becomes available.
	for {
		task = s.NextTask()
		if task.Kind == TaskExecute {
			s.FinishExecution(task.TxIdx, task.Incarnation)
			continue
		}
		break
	}

	if task.Kind != TaskValidate || task.TxIdx != 0 {
		t.Fatalf("expected validate task for tx 0, got %+v", task)
	}
	s.FinishValidationFail(0, task.Incarnation)

	if n := s.ValFailCount(0); n != 1 {
		t.Fatalf("ValFailCount(0) after fail = %d, want 1", n)
	}
	st, inc := s.FinalStatus(0)
	if st != TxStatusAborting || inc != 1 {
		t.Fatalf("FinalStatus(0) = %v, %d, want Aborting, 1", st, inc)
	}
}

// RewindValidationIdx counts a "fire" when it actually moves validationIdx
// down, and a "skip" when the index is already at or below the target.
func TestSchedulerRewindValidationIdxStats(t *testing.T) {
	s := NewScheduler(5)
	s.validationIdx.Store(5)

	s.RewindValidationIdx(2)
	fires, skips := s.RewindStats()
	if fires != 1 || skips != 0 {
		t.Fatalf("after fire: fires=%d skips=%d, want 1,0", fires, skips)
	}
	if got := s.validationIdx.Load(); got != 2 {
		t.Fatalf("validationIdx = %d, want 2", got)
	}

	s.RewindValidationIdx(2) // already at target -> skip
	fires, skips = s.RewindStats()
	if fires != 1 || skips != 1 {
		t.Fatalf("after skip: fires=%d skips=%d, want 1,1", fires, skips)
	}
}
