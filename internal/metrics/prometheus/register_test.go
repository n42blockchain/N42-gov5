// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package prometheus

import (
	"runtime"
	"sync"
	"testing"
)

// See common/metrics/register_test.go for the bug analysis. This file
// is a duplicate of the same regression suite for the parallel
// register.go in this package — both files implement the same fix and
// must be guarded against the same regression.

func TestGetOrCreateSummary_NoLeak(t *testing.T) {
	const name = "test_summary_no_leak{a=\"1\"}"

	first := GetOrCreateSummary(name)
	if first == nil {
		t.Fatal("first call returned nil")
	}

	runtime.GC()
	g0 := runtime.NumGoroutine()

	for i := 0; i < 1000; i++ {
		got := GetOrCreateSummary(name)
		if got != first {
			t.Fatalf("iter %d: got different Summary instance (was %p, got %p)", i, first, got)
		}
	}

	runtime.GC()
	g1 := runtime.NumGoroutine()
	if delta := g1 - g0; delta > 10 {
		t.Fatalf("goroutine leak suspected: before=%d after=%d delta=%d", g0, g1, delta)
	}
}

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
