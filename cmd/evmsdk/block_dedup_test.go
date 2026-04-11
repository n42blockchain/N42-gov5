// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBlockDedup_FirstMarkReturnsTrue(t *testing.T) {
	d := NewBlockDedup(8)
	if !d.Mark(1, types.Hash{0x1}) {
		t.Fatal("first Mark should return true")
	}
	if d.Mark(1, types.Hash{0x1}) {
		t.Fatal("repeat Mark should return false")
	}
}

func TestBlockDedup_DifferentHashSameNumber(t *testing.T) {
	d := NewBlockDedup(8)
	if !d.Mark(100, types.Hash{0xa}) {
		t.Fatal("hash a first time")
	}
	// Same number, different hash (a fork) is a NEW entry.
	if !d.Mark(100, types.Hash{0xb}) {
		t.Fatal("hash b at same number should be treated as new")
	}
	if d.Mark(100, types.Hash{0xa}) {
		t.Fatal("hash a still in window")
	}
}

func TestBlockDedup_LRUEviction(t *testing.T) {
	d := NewBlockDedup(4)
	for i := uint64(1); i <= 4; i++ {
		if !d.Mark(i, types.Hash{byte(i)}) {
			t.Fatalf("entry %d should be new", i)
		}
	}
	if d.Len() != 4 {
		t.Fatalf("len = %d, want 4", d.Len())
	}
	// Add a 5th — entry 1 should be evicted.
	if !d.Mark(5, types.Hash{0x5}) {
		t.Fatal("entry 5 should be new")
	}
	if d.Has(1, types.Hash{0x1}) {
		t.Fatal("entry 1 should have been evicted")
	}
	if !d.Has(2, types.Hash{0x2}) {
		t.Fatal("entry 2 should still be present")
	}
	// And re-marking the evicted entry returns true (new again).
	if !d.Mark(1, types.Hash{0x1}) {
		t.Fatal("re-Mark of evicted entry should return true")
	}
}

func TestBlockDedup_RingWraparound(t *testing.T) {
	d := NewBlockDedup(3)
	// Cycle through the ring 3 times.
	for cycle := 0; cycle < 3; cycle++ {
		base := uint64(cycle * 100)
		for i := uint64(1); i <= 3; i++ {
			d.Mark(base+i, types.Hash{byte(i)})
		}
	}
	// Only the LAST cycle should be present.
	if d.Has(101, types.Hash{0x1}) {
		t.Error("cycle 1 should be evicted")
	}
	if !d.Has(201, types.Hash{0x1}) {
		t.Error("cycle 2 entry 1 should be present")
	}
	if !d.Has(203, types.Hash{0x3}) {
		t.Error("cycle 2 entry 3 should be present")
	}
}

func TestBlockDedup_Reset(t *testing.T) {
	d := NewBlockDedup(8)
	d.Mark(1, types.Hash{0x1})
	d.Mark(2, types.Hash{0x2})
	if d.Len() != 2 {
		t.Fatal("len before reset")
	}
	d.Reset()
	if d.Len() != 0 {
		t.Fatal("len after reset should be 0")
	}
	if d.Has(1, types.Hash{0x1}) {
		t.Fatal("Has after reset should be false")
	}
	// Ring index resets too — first Mark after reset starts at slot 0.
	if !d.Mark(1, types.Hash{0x1}) {
		t.Fatal("Mark after reset should return true")
	}
}

func TestBlockDedup_ZeroCapacityClamped(t *testing.T) {
	d := NewBlockDedup(0)
	// Capacity defaults; should at least hold a few entries without panic.
	for i := uint64(1); i <= 100; i++ {
		d.Mark(i, types.Hash{byte(i % 256)})
	}
	if d.Len() == 0 {
		t.Fatal("zero capacity should default, not noop")
	}
}

func TestBlockDedup_ConcurrentMark(t *testing.T) {
	d := NewBlockDedup(256)
	// 4 goroutines trying to mark the same set; total trues across all
	// goroutines must equal the number of unique entries.
	const numEntries = 200
	const goroutines = 4
	results := make(chan int, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			trueCount := 0
			for i := uint64(1); i <= numEntries; i++ {
				if d.Mark(i, types.Hash{byte(i)}) {
					trueCount++
				}
			}
			results <- trueCount
		}()
	}
	totalTrues := 0
	for g := 0; g < goroutines; g++ {
		totalTrues += <-results
	}
	if totalTrues != numEntries {
		t.Errorf("total trues = %d, want %d (each entry must be marked exactly once)", totalTrues, numEntries)
	}
}
