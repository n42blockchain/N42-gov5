// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import "testing"

func TestDeferredDepthAtDefaultPrev(t *testing.T) {
	defer SetDeferredDepthForTest(2, 10)()
	for n, want := range map[uint64]int{0: 1, 9: 1, 10: 2, 11: 2, 1 << 40: 2} {
		if got := DeferredDepthAt(n); got != want {
			t.Fatalf("DeferredDepthAt(%d) = %d, want %d", n, got, want)
		}
	}
	if DeferredDepthPrev() != 1 {
		t.Fatalf("prev = %d, want 1", DeferredDepthPrev())
	}
}

func TestDeferredDepthAtTransition(t *testing.T) {
	defer SetDeferredDepthTransitionForTest(2, 3, 100)()
	for n, want := range map[uint64]int{0: 2, 1: 2, 99: 2, 100: 3, 101: 3} {
		if got := DeferredDepthAt(n); got != want {
			t.Fatalf("DeferredDepthAt(%d) = %d, want %d", n, got, want)
		}
	}
	if !LagMode() || DeferredDepth() != 3 || DeferredDepthPrev() != 2 || DeferredDepthFrom() != 100 {
		t.Fatalf("lag %v depth %d prev %d from %d", LagMode(), DeferredDepth(), DeferredDepthPrev(), DeferredDepthFrom())
	}
	// The boundary rule: target t(n) = n - k(n) never decreases and never
	// skips a block; FROM-1 and FROM report the same block.
	prevT := uint64(0)
	for n := uint64(3); n < 120; n++ {
		tn := n - uint64(DeferredDepthAt(n))
		if n > 3 {
			if tn < prevT || tn > prevT+1 {
				t.Fatalf("t(%d) = %d after t(%d) = %d", n, tn, n-1, prevT)
			}
			if (tn == prevT) != (n == 100) {
				t.Fatalf("t(%d) = %d repeats only at FROM", n, tn)
			}
		}
		prevT = tn
	}
}

func TestDeferredDepthPrevClamped(t *testing.T) {
	defer SetDeferredDepthTransitionForTest(5, 2, 50)()
	if got := DeferredDepthAt(10); got != 2 {
		t.Fatalf("a previous depth above the depth is clamped: got %d, want 2", got)
	}
	SetDeferredDepthTransitionForTest(0, 2, 50)
	if got := DeferredDepthAt(10); got != 1 {
		t.Fatalf("a previous depth below 1 is clamped: got %d, want 1", got)
	}
}

func TestClampPrev(t *testing.T) {
	for _, c := range [][3]int64{{1, 3, 1}, {2, 3, 2}, {3, 3, 3}, {4, 3, 3}, {0, 3, 1}, {-1, 2, 1}} {
		if got := clampPrev(c[0], c[1]); got != c[2] {
			t.Fatalf("clampPrev(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}
