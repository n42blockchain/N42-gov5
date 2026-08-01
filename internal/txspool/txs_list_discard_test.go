// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Discard used to size its result slice by the caller's `slots`, which is the
// pool's projected overflow — hundreds of thousands of entries under load, for
// a loop that usually drops a handful. These tests pin both halves of the fix:
// the behaviour must be identical, and the allocation must track the
// transactions actually dropped rather than the overflow figure.

package txspool

import (
	"testing"
)

// seedPriced fills a priced list with n remote transactions and registers them
// in the lookup, so Discard sees them as live rather than stale.
func seedPriced(n int) (*txLookup, *txPricedList) {
	lookup := newTxLookup()
	priced := newTxPricedList(lookup)
	for i := 0; i < n; i++ {
		tx := newTestTx(uint64(i), uint64(100+i))
		lookup.Add(tx, false)
		priced.Put(tx, false)
	}
	return lookup, priced
}

// TestDiscardAllocationTracksDropped is the regression: ask for a huge overflow
// on a list that can only yield a few transactions, and the allocation must
// stay proportional to what came back, not to what was asked for.
func TestDiscardAllocationTracksDropped(t *testing.T) {
	const listSize = 8
	const hugeSlots = 400_000 // what a badly overflowing pool passes

	_, priced := seedPriced(listSize)

	allocs := testing.AllocsPerRun(20, func() {
		_, priced2 := seedPriced(listSize)
		priced2.Discard(hugeSlots, true)
	})
	// Sizing by slots allocated 400k pointers (3.2 MB) per call; the seeding
	// itself dominates this figure now, which is the point — the Discard slice
	// is no longer the story.
	t.Logf("allocs per (seed + Discard) run: %.0f", allocs)

	drop, ok := priced.Discard(hugeSlots, true)
	if !ok {
		t.Fatal("force=true must always succeed")
	}
	if len(drop) > listSize {
		t.Fatalf("dropped %d transactions from a list of %d", len(drop), listSize)
	}
	if cap(drop) > listSize+discardPrealloc {
		t.Fatalf("result capacity %d is far beyond what was dropped (%d) — "+
			"the slice is still being sized by the caller's slots", cap(drop), len(drop))
	}
}

// TestDiscardBehaviourUnchanged checks the two contracts the caller relies on:
// force=false rolls everything back when it cannot free enough, force=true
// takes what it can get.
func TestDiscardBehaviourUnchanged(t *testing.T) {
	t.Run("cannot satisfy without force", func(t *testing.T) {
		_, priced := seedPriced(4)
		before := len(priced.urgent.list) + len(priced.floating.list)
		drop, ok := priced.Discard(400_000, false)
		if ok || drop != nil {
			t.Fatalf("expected failure, got ok=%v drop=%d", ok, len(drop))
		}
		if after := len(priced.urgent.list) + len(priced.floating.list); after != before {
			t.Fatalf("failed Discard must put everything back: %d -> %d", before, after)
		}
	})

	t.Run("satisfiable request", func(t *testing.T) {
		_, priced := seedPriced(64)
		drop, ok := priced.Discard(3, false)
		if !ok {
			t.Fatal("a request the list can satisfy must succeed")
		}
		if len(drop) == 0 {
			t.Fatal("nothing was dropped")
		}
	})

	t.Run("force takes what it can", func(t *testing.T) {
		_, priced := seedPriced(4)
		drop, ok := priced.Discard(400_000, true)
		if !ok {
			t.Fatal("force=true must succeed")
		}
		if len(drop) == 0 {
			t.Fatal("force=true dropped nothing from a non-empty list")
		}
	})

	t.Run("empty list", func(t *testing.T) {
		_, priced := seedPriced(0)
		drop, ok := priced.Discard(400_000, true)
		if !ok || len(drop) != 0 {
			t.Fatalf("empty list: ok=%v drop=%d", ok, len(drop))
		}
	})
}

// TestDiscardNegativeSlots: make() panics on a negative capacity, so a caller
// whose overflow arithmetic goes negative used to take the process down.
func TestDiscardNegativeSlots(t *testing.T) {
	_, priced := seedPriced(4)
	drop, ok := priced.Discard(-5, true)
	if !ok {
		t.Fatal("a negative request needs no work and must succeed")
	}
	if len(drop) != 0 {
		t.Fatalf("negative request dropped %d transactions", len(drop))
	}
}
