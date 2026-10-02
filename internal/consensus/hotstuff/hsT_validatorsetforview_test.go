// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import "testing"

// TestValidatorSetForView_EpochBoundaryBranches covers every branch of
// ValidatorSetForView once epochs are enabled: current-set (post-boundary),
// prevSet (recent cross-boundary), historicalSets (deeper history), and the
// final currentSet fallback when nothing else matches.
func TestValidatorSetForView_EpochBoundaryBranches(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManagerWithLength(setup.vs, 100)
	em.setSinceView = 50

	t.Run("post-boundary returns currentSet", func(t *testing.T) {
		if got := em.ValidatorSetForView(50); got != em.currentSet {
			t.Fatalf("ValidatorSetForView(setSinceView) = %v, want currentSet", got)
		}
		if got := em.ValidatorSetForView(60); got != em.currentSet {
			t.Fatalf("ValidatorSetForView(past boundary) = %v, want currentSet", got)
		}
	})

	otherSetup := newTestSetup(t, 3)
	t.Run("pre-boundary with prevSet returns prevSet", func(t *testing.T) {
		em.prevSet = otherSetup.vs
		if got := em.ValidatorSetForView(10); got != otherSetup.vs {
			t.Fatalf("ValidatorSetForView(pre-boundary) = %v, want prevSet", got)
		}
		em.prevSet = nil
	})

	histSetup := newTestSetup(t, 2)
	t.Run("pre-boundary without prevSet falls back to historicalSets", func(t *testing.T) {
		epoch := em.EpochForView(10)
		em.historicalSets[epoch] = histSetup.vs
		if got := em.ValidatorSetForView(10); got != histSetup.vs {
			t.Fatalf("ValidatorSetForView(historical) = %v, want the historical set", got)
		}
		delete(em.historicalSets, epoch)
	})

	t.Run("pre-boundary with nothing recorded falls back to currentSet", func(t *testing.T) {
		if got := em.ValidatorSetForView(10); got != em.currentSet {
			t.Fatalf("ValidatorSetForView(no history) = %v, want currentSet fallback", got)
		}
	})
}
