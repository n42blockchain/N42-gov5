// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestByteLRUGrowDeleteResetStats drives the byteLRU past its initial slot
// count (forcing growLocked/allocSlot to run), then exercises Delete,
// DeleteBatch, Reset, Stats, and ResetStats.
func TestByteLRUGrowDeleteResetStats(t *testing.T) {
	// A generous byte budget raises maxCap well past initialCacheSlots
	// (1024) so inserting more than that forces growLocked to double cap.
	c := newByteLRU[types.Hash](10 * 1024 * 1024)

	const n = 1500
	keys := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		var k types.Hash
		k[0] = byte(i)
		k[1] = byte(i >> 8)
		keys[i] = k
		c.Put(k, []byte{byte(i)}, 8)
	}

	if c.cap <= initialCacheSlots {
		t.Fatalf("cap = %d, want > %d after growth", c.cap, initialCacheSlots)
	}

	hits, misses, bytes, entries := c.Stats()
	if entries != n {
		t.Fatalf("Stats entries = %d, want %d", entries, n)
	}
	if bytes <= 0 {
		t.Fatalf("Stats bytes = %d, want > 0", bytes)
	}
	_ = hits
	_ = misses

	// Read back a sample to generate a hit, and a miss for an absent key.
	if _, ok := c.Get(keys[0]); !ok {
		t.Fatal("Get(keys[0]) miss, want hit")
	}
	var absent types.Hash
	absent[31] = 0xFF
	if _, ok := c.Get(absent); ok {
		t.Fatal("Get(absent) hit, want miss")
	}
	hits, misses, _, _ = c.Stats()
	if hits == 0 || misses == 0 {
		t.Fatalf("Stats after Get = hits %d misses %d, want both > 0", hits, misses)
	}

	c.ResetStats()
	hits, misses, _, _ = c.Stats()
	if hits != 0 || misses != 0 {
		t.Fatalf("Stats after ResetStats = %d, %d, want 0, 0", hits, misses)
	}

	// Delete a single key.
	if !c.Delete(keys[1]) {
		t.Fatal("Delete(keys[1]) = false, want true")
	}
	if _, ok := c.Get(keys[1]); ok {
		t.Fatal("Get after Delete still hits")
	}
	if c.Delete(keys[1]) {
		t.Fatal("Delete on an already-removed key returned true")
	}

	// DeleteBatch a slice of keys.
	c.DeleteBatch(keys[2:10])
	for _, k := range keys[2:10] {
		if _, ok := c.Get(k); ok {
			t.Fatalf("Get(%v) hit after DeleteBatch", k)
		}
	}

	// Reset drops everything.
	c.Reset()
	_, _, bytes, entries = c.Stats()
	if entries != 0 || bytes != 0 {
		t.Fatalf("Stats after Reset = entries %d bytes %d, want 0, 0", entries, bytes)
	}
	if _, ok := c.Get(keys[500]); ok {
		t.Fatal("Get after Reset still hits")
	}

	// The cache is usable again after Reset.
	c.Put(keys[0], []byte{1}, 1)
	if _, ok := c.Get(keys[0]); !ok {
		t.Fatal("Get after re-Put following Reset missed")
	}
}
