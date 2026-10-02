// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "testing"

// NOTE (defect, not fixed here per instructions): beginExecution is dead
// code -- grepping the package, nothing calls it. NextTask dispatches
// TaskExecute by setting status directly rather than through
// beginExecution, so this transition helper (and its Ready/Aborting guard)
// never runs in production. Tested directly against the unexported method
// so the behavior it documents doesn't silently rot.
func TestSchedulerBeginExecution(t *testing.T) {
	s := NewScheduler(3)

	// Fresh tx is Ready; beginExecution transitions it to Executing and
	// returns its (zero) incarnation.
	if inc := s.beginExecution(0); inc != 0 {
		t.Fatalf("beginExecution(fresh) incarnation = %d, want 0", inc)
	}
	st, _ := s.Status(0)
	if st != TxStatusExecuting {
		t.Fatalf("status after beginExecution = %v, want Executing", st)
	}

	// Calling again while Executing is a documented no-op on status (guard
	// only fires from Ready/Aborting) but still returns the incarnation.
	if inc := s.beginExecution(0); inc != 0 {
		t.Fatalf("beginExecution(already executing) incarnation = %d, want 0", inc)
	}
	st, _ = s.Status(0)
	if st != TxStatusExecuting {
		t.Fatalf("status unexpectedly changed: %v", st)
	}

	// Simulate an abort: bump incarnation and move status to Aborting
	// directly (mirrors what FinishExecution/validation failure would do),
	// then confirm beginExecution picks it back up as Executing with the
	// bumped incarnation.
	s.txMu[1].Lock()
	s.incarnation[1] = 2
	s.status[1] = TxStatusAborting
	s.txMu[1].Unlock()

	if inc := s.beginExecution(1); inc != 2 {
		t.Fatalf("beginExecution(aborting) incarnation = %d, want 2", inc)
	}
	st, incv := s.Status(1)
	if st != TxStatusExecuting || incv != 2 {
		t.Fatalf("status/incarnation after re-execution = %v/%d, want Executing/2", st, incv)
	}

	// getIncarnation reads without mutating status.
	if got := s.getIncarnation(1); got != 2 {
		t.Fatalf("getIncarnation = %d, want 2", got)
	}
	st, _ = s.Status(1)
	if st != TxStatusExecuting {
		t.Fatalf("getIncarnation mutated status to %v", st)
	}

	// A Committed tx is left untouched by beginExecution (guard only covers
	// Ready/Aborting).
	s.txMu[2].Lock()
	s.status[2] = TxStatusCommitted
	s.txMu[2].Unlock()
	s.beginExecution(2)
	st, _ = s.Status(2)
	if st != TxStatusCommitted {
		t.Fatalf("beginExecution changed a Committed tx's status to %v", st)
	}
}
