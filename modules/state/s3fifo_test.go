// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestS3FIFOGrowEvictGhostReadmission drives the cache through growth
// (growLocked), S/M eviction (evictSLocked/evictMLocked), ghost tracking
// (addGhost), and ghost re-admission (removeGhost) by inserting well past
// the small budget, then re-Putting an evicted key.
func TestS3FIFOGrowEvictGhostReadmission(t *testing.T) {
	// A small byte budget keeps maxCap modest so eviction kicks in quickly,
	// while still exceeding initialCacheSlots (1024) to exercise growLocked.
	c := newS3FIFO[types.Hash](200*1024, 32)

	const n = 3000
	keys := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		var k types.Hash
		k[0] = byte(i)
		k[1] = byte(i >> 8)
		k[2] = byte(i >> 16)
		keys[i] = k
		c.Put(k, []byte{byte(i)}, 1)
		// Re-touch every 7th key immediately so some S entries earn
		// freq>0 and get promoted to M on eviction (evictSLocked's
		// promote branch) rather than only dropping to ghost.
		if i%7 == 0 {
			c.Get(k)
		}
	}

	if c.cap <= initialCacheSlots {
		t.Fatalf("cap = %d, want > %d after growth", c.cap, initialCacheSlots)
	}

	hits, misses, bytes, entries := c.Stats()
	if entries == 0 {
		t.Fatal("Stats entries = 0, want > 0")
	}
	if bytes <= 0 {
		t.Fatal("Stats bytes <= 0, want > 0")
	}
	if hits == 0 {
		t.Fatal("Stats hits = 0, want > 0 (periodic re-Get)")
	}
	_ = misses

	// The earliest keys should have been evicted (cache is far smaller
	// than n) and landed in the ghost ring. Re-Put one to trigger
	// removeGhost + direct-to-M re-admission.
	evicted := keys[1] // i=1 is never re-Get'd (only i%7==0 is), so it's a
	// one-touch S entry that drops straight to ghost rather than being
	// promoted to M.
	if _, ok := c.Get(evicted); ok {
		t.Fatal("expected keys[1] to have been evicted under this fill pressure")
	}
	c.Put(evicted, []byte{0xEE}, 1)
	if v, ok := c.Get(evicted); !ok || len(v) == 0 {
		t.Fatal("re-Put of an evicted (ghosted) key did not land back in the cache")
	}

	// Range visits live entries.
	visited := 0
	c.Range(func(types.Hash) bool {
		visited++
		return true
	})
	if visited == 0 {
		t.Fatal("Range visited 0 entries")
	}

	// Delete / DeleteBatch / Reset / ResetStats round out the surface.
	if !c.Delete(evicted) {
		t.Fatal("Delete(evicted) = false, want true")
	}
	c.DeleteBatch(keys[n-10 : n])

	c.ResetStats()
	hits, misses, _, _ = c.Stats()
	if hits != 0 || misses != 0 {
		t.Fatalf("Stats after ResetStats = %d, %d, want 0, 0", hits, misses)
	}

	c.Reset()
	_, _, bytes, entries = c.Stats()
	if entries != 0 || bytes != 0 {
		t.Fatalf("Stats after Reset = entries %d bytes %d, want 0, 0", entries, bytes)
	}
}
