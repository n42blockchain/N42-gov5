// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func hashFromByte(b byte) types.Hash {
	var h types.Hash
	h[0] = b
	return h
}

func TestCodeCache_InsertGet(t *testing.T) {
	cache := NewCodeCache(10)

	h := hashFromByte(0x01)
	code := []byte{0x60, 0x00, 0xFD}

	// Not present initially.
	if _, ok := cache.Get(h); ok {
		t.Fatal("Get() should return false for missing entry")
	}

	cache.Insert(h, code)

	got, ok := cache.Get(h)
	if !ok {
		t.Fatal("Get() should return true after Insert")
	}
	if !bytes.Equal(got, code) {
		t.Errorf("Get() = %x, want %x", got, code)
	}

	if cache.Len() != 1 {
		t.Errorf("Len() = %d, want 1", cache.Len())
	}

	if !cache.Contains(h) {
		t.Error("Contains() = false, want true")
	}

	// Remove.
	cache.Remove(h)
	if cache.Contains(h) {
		t.Error("Contains() = true after Remove, want false")
	}
	if cache.Len() != 0 {
		t.Errorf("Len() = %d after Remove, want 0", cache.Len())
	}
}

func TestCodeCache_LRUEviction(t *testing.T) {
	cache := NewCodeCache(3)

	h1 := hashFromByte(0x01)
	h2 := hashFromByte(0x02)
	h3 := hashFromByte(0x03)
	h4 := hashFromByte(0x04)

	cache.Insert(h1, []byte{0x01})
	cache.Insert(h2, []byte{0x02})
	cache.Insert(h3, []byte{0x03})

	if cache.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", cache.Len())
	}

	// Inserting h4 should evict h1 (LRU).
	cache.Insert(h4, []byte{0x04})

	if cache.Len() != 3 {
		t.Fatalf("Len() = %d, want 3 after eviction", cache.Len())
	}

	if cache.Contains(h1) {
		t.Error("h1 should have been evicted")
	}
	if !cache.Contains(h2) {
		t.Error("h2 should still be present")
	}
	if !cache.Contains(h3) {
		t.Error("h3 should still be present")
	}
	if !cache.Contains(h4) {
		t.Error("h4 should be present")
	}

	// Access h2 to make it most-recently-used, then insert two more.
	cache.Get(h2)
	cache.Insert(hashFromByte(0x05), []byte{0x05}) // evicts h3
	cache.Insert(hashFromByte(0x06), []byte{0x06}) // evicts h4

	if !cache.Contains(h2) {
		t.Error("h2 should still be present after access refreshed it")
	}
	if cache.Contains(h3) {
		t.Error("h3 should have been evicted")
	}
	if cache.Contains(h4) {
		t.Error("h4 should have been evicted")
	}
}

func TestCodeCache_Clear(t *testing.T) {
	cache := NewCodeCache(10)

	cache.Insert(hashFromByte(0x01), []byte{0x01})
	cache.Insert(hashFromByte(0x02), []byte{0x02})

	cache.Clear()

	if cache.Len() != 0 {
		t.Errorf("Len() = %d after Clear, want 0", cache.Len())
	}
}

func TestCodeCache_UpdateExisting(t *testing.T) {
	cache := NewCodeCache(10)

	h := hashFromByte(0x01)
	cache.Insert(h, []byte{0x01})
	cache.Insert(h, []byte{0x02}) // update

	got, ok := cache.Get(h)
	if !ok {
		t.Fatal("Get() should return true")
	}
	if !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("Get() = %x, want 02 after update", got)
	}
	if cache.Len() != 1 {
		t.Errorf("Len() = %d, want 1 (no duplicate)", cache.Len())
	}
}

func TestHotContractTracker_TopContracts(t *testing.T) {
	tracker := NewHotContractTracker()

	h1 := hashFromByte(0x01)
	h2 := hashFromByte(0x02)
	h3 := hashFromByte(0x03)

	// h1 accessed 3 times, h2 accessed 5 times, h3 accessed 1 time.
	tracker.RecordBlockAccesses([]types.Hash{h1, h2, h3})
	tracker.RecordBlockAccesses([]types.Hash{h1, h2})
	tracker.RecordBlockAccesses([]types.Hash{h1, h2})
	tracker.RecordBlockAccesses([]types.Hash{h2})
	tracker.RecordBlockAccesses([]types.Hash{h2})

	if tracker.BlocksTracked() != 5 {
		t.Errorf("BlocksTracked() = %d, want 5", tracker.BlocksTracked())
	}

	top := tracker.TopContracts(2)
	if len(top) != 2 {
		t.Fatalf("TopContracts(2) len = %d, want 2", len(top))
	}
	if top[0] != h2 {
		t.Errorf("TopContracts(2)[0] = %x, want h2 (most accessed)", top[0])
	}
	if top[1] != h1 {
		t.Errorf("TopContracts(2)[1] = %x, want h1 (second most accessed)", top[1])
	}

	// Request more than available.
	all := tracker.TopContracts(100)
	if len(all) != 3 {
		t.Errorf("TopContracts(100) len = %d, want 3", len(all))
	}
}

func TestHotContractTracker_Decay(t *testing.T) {
	tracker := NewHotContractTracker()

	h1 := hashFromByte(0x01)
	h2 := hashFromByte(0x02)

	// h1: 10 accesses, h2: 1 access.
	for i := 0; i < 10; i++ {
		tracker.RecordBlockAccesses([]types.Hash{h1})
	}
	tracker.RecordBlockAccesses([]types.Hash{h2})

	// Decay by 0.5: h1 -> 5, h2 -> 0 (removed).
	tracker.Decay(0.5)

	top := tracker.TopContracts(10)
	if len(top) != 1 {
		t.Fatalf("after decay, TopContracts len = %d, want 1", len(top))
	}
	if top[0] != h1 {
		t.Errorf("after decay, top contract = %x, want h1", top[0])
	}

	// Decay again: h1: 5 * 0.5 = 2.
	tracker.Decay(0.5)
	top = tracker.TopContracts(10)
	if len(top) != 1 {
		t.Fatalf("after second decay, TopContracts len = %d, want 1", len(top))
	}

	// Decay to zero: h1: 2 * 0.1 = 0 (removed).
	tracker.Decay(0.1)
	top = tracker.TopContracts(10)
	if len(top) != 0 {
		t.Errorf("after full decay, TopContracts len = %d, want 0", len(top))
	}
}

func TestHotContractTracker_EmptyTopContracts(t *testing.T) {
	tracker := NewHotContractTracker()
	top := tracker.TopContracts(5)
	if len(top) != 0 {
		t.Errorf("TopContracts on empty tracker len = %d, want 0", len(top))
	}
	if tracker.BlocksTracked() != 0 {
		t.Errorf("BlocksTracked() = %d, want 0", tracker.BlocksTracked())
	}
}
