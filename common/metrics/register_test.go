// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package prometheus

import (
	"runtime"
	"sync"
	"testing"
)

// TestGetOrCreateSummary_NoLeak verifies the fix for the goroutine /
// data-staleness bug at register.go:GetOrCreateSummary. Without the
// sync.Once cache, every call would (1) spawn a new summariesSwapCron
// goroutine via VM's registerSummaryLocked, and (2) silently drop
// updates because DefaultRegistry kept the first Summary while the
// caller used a fresh one.
func TestGetOrCreateSummary_NoLeak(t *testing.T) {
	const name = "test_summary_no_leak{a=\"1\"}"

	first := GetOrCreateSummary(name)
	if first == nil {
		t.Fatal("first call returned nil")
	}

	runtime.GC()
	g0 := runtime.NumGoroutine()

	const iters = 1000
	for i := 0; i < iters; i++ {
		got := GetOrCreateSummary(name)
		if got != first {
			t.Fatalf("iter %d: got different Summary instance (was %p, got %p)", i, first, got)
		}
	}

	runtime.GC()
	g1 := runtime.NumGoroutine()
	// Allow some noise (test runner spawns goroutines for parallel tests),
	// but a leak would manifest as 1000+ extra goroutines.
	if delta := g1 - g0; delta > 10 {
		t.Fatalf("goroutine leak suspected: before=%d after=%d delta=%d", g0, g1, delta)
	}
}

// TestGetOrCreateCounter_NoLeak — same regression guard for counters.
// Counters don't spawn cron goroutines (only summaries do), so this test
// catches the data-staleness side of the bug: ensure repeated calls
// return the SAME Counter so updates land in the registry-tracked one.
func TestGetOrCreateCounter_NoLeak(t *testing.T) {
	const name = "test_counter_no_leak{a=\"1\"}"

	first := GetOrCreateCounter(name)
	if first == nil {
		t.Fatal("first call returned nil")
	}

	for i := 0; i < 1000; i++ {
		got := GetOrCreateCounter(name)
		if got != first {
			t.Fatalf("iter %d: got different Counter instance (was %p, got %p)", i, first, got)
		}
	}
}

// TestGetOrCreate_RaceFree exercises the sync.Once-per-name path under
// concurrent access. Race-detector should see no data race; result map
// should converge to a single instance per name.
func TestGetOrCreate_RaceFree(t *testing.T) {
	const goroutines = 32
	const iters = 100
	const name = "test_race_free_summary{a=\"1\"}"

	var wg sync.WaitGroup
	results := make([]Summary, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var last Summary
			for i := 0; i < iters; i++ {
				last = GetOrCreateSummary(name)
			}
			results[idx] = last
		}(g)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("got nil Summary")
	}
	for i, r := range results {
		if r != first {
			t.Fatalf("goroutine %d: got different Summary instance (was %p, got %p)", i, first, r)
		}
	}
}
