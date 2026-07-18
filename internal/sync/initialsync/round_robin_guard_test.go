// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package initialsync

import "testing"

// TestNoProgressGuard covers the orphan-spin guard: consecutive parent-missing
// batches with no head advance must trip after maxNoProgressBatches, while any
// head progress (or a non-orphan outcome) resets the counter.
func TestNoProgressGuard(t *testing.T) {
	t.Run("orphan batches trip after threshold", func(t *testing.T) {
		g := noProgressGuard{lastHead: 100}
		for i := 1; i < maxNoProgressBatches; i++ {
			if g.observe(100, true) {
				t.Fatalf("aborted early at %d", i)
			}
		}
		if !g.observe(100, true) {
			t.Fatal("did not abort at threshold")
		}
	})

	t.Run("head progress resets the counter", func(t *testing.T) {
		g := noProgressGuard{lastHead: 100}
		for i := 0; i < maxNoProgressBatches-1; i++ {
			g.observe(100, true)
		}
		// One block of progress resets it.
		if g.observe(101, false) {
			t.Fatal("progress should not abort")
		}
		if g.count != 0 {
			t.Fatalf("counter not reset: %d", g.count)
		}
		// It now takes a full fresh run of orphan batches to trip again.
		for i := 1; i < maxNoProgressBatches; i++ {
			if g.observe(101, true) {
				t.Fatalf("aborted early after reset at %d", i)
			}
		}
		if !g.observe(101, true) {
			t.Fatal("did not abort after reset run")
		}
	})

	t.Run("non-orphan no-progress does not trip", func(t *testing.T) {
		g := noProgressGuard{lastHead: 100}
		for i := 0; i < maxNoProgressBatches*2; i++ {
			if g.observe(100, false) {
				t.Fatal("non-orphan stall must not abort")
			}
		}
	})
}
