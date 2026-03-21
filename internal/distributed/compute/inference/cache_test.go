// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package inference

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func makeHash(b byte) types.Hash {
	var h types.Hash
	h[0] = b
	return h
}

func TestResultCache_PutGet(t *testing.T) {
	cache := NewResultCache(100, 1*time.Hour)

	reqID := makeHash(0x01)
	outputCAS := makeHash(0xAA)

	cache.Put(reqID, RequestVerified, outputCAS)

	got, ok := cache.Get(reqID)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Status != RequestVerified {
		t.Fatalf("status = %v, want Verified", got.Status)
	}
	if got.OutputCAS != outputCAS {
		t.Fatalf("outputCAS = %s, want %s", got.OutputCAS.Hex(), outputCAS.Hex())
	}
	if got.RequestID != reqID {
		t.Fatalf("requestID mismatch")
	}

	// Non-existent key.
	_, ok = cache.Get(makeHash(0xFF))
	if ok {
		t.Fatal("expected cache miss for non-existent key")
	}
}

func TestResultCache_Eviction(t *testing.T) {
	cache := NewResultCache(3, 1*time.Hour)

	// Fill to capacity.
	cache.Put(makeHash(0x01), RequestPending, types.Hash{})
	cache.Put(makeHash(0x02), RequestPending, types.Hash{})
	cache.Put(makeHash(0x03), RequestPending, types.Hash{})

	if cache.Size() != 3 {
		t.Fatalf("size = %d, want 3", cache.Size())
	}

	// Adding a 4th should evict the oldest (0x01).
	cache.Put(makeHash(0x04), RequestVerified, types.Hash{})

	if cache.Size() != 3 {
		t.Fatalf("size after eviction = %d, want 3", cache.Size())
	}

	_, ok := cache.Get(makeHash(0x01))
	if ok {
		t.Fatal("oldest entry (0x01) should have been evicted")
	}

	_, ok = cache.Get(makeHash(0x04))
	if !ok {
		t.Fatal("newest entry (0x04) should be present")
	}
}

func TestResultCache_TTLExpiry(t *testing.T) {
	// Use a very short TTL.
	cache := NewResultCache(100, 1*time.Millisecond)

	reqID := makeHash(0x01)
	cache.Put(reqID, RequestVerified, types.Hash{})

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	_, ok := cache.Get(reqID)
	if ok {
		t.Fatal("expired entry should not be returned")
	}

	// Size should reflect removal on access.
	if cache.Size() != 0 {
		t.Fatalf("size = %d, want 0 after expired entry removal", cache.Size())
	}
}

func TestResultCache_Delete(t *testing.T) {
	cache := NewResultCache(100, 1*time.Hour)

	reqID := makeHash(0x01)
	cache.Put(reqID, RequestPending, types.Hash{})

	if cache.Size() != 1 {
		t.Fatalf("size = %d, want 1", cache.Size())
	}

	cache.Delete(reqID)

	if cache.Size() != 0 {
		t.Fatalf("size after delete = %d, want 0", cache.Size())
	}

	_, ok := cache.Get(reqID)
	if ok {
		t.Fatal("deleted entry should not be found")
	}

	// Deleting non-existent should be safe.
	cache.Delete(makeHash(0xFF))
}

func TestResultCache_Prune(t *testing.T) {
	cache := NewResultCache(100, 1*time.Millisecond)

	cache.Put(makeHash(0x01), RequestPending, types.Hash{})
	cache.Put(makeHash(0x02), RequestVerified, types.Hash{})
	cache.Put(makeHash(0x03), RequestFailed, types.Hash{})

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	pruned := cache.Prune()
	if pruned != 3 {
		t.Fatalf("pruned = %d, want 3", pruned)
	}

	if cache.Size() != 0 {
		t.Fatalf("size after prune = %d, want 0", cache.Size())
	}

	// Prune with no expired entries.
	cache2 := NewResultCache(100, 1*time.Hour)
	cache2.Put(makeHash(0x01), RequestPending, types.Hash{})
	pruned = cache2.Prune()
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (nothing expired)", pruned)
	}
}

func TestResultCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := NewResultCache(100, 1*time.Hour)

	const goroutines = 10
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				h := makeHash(byte((id*opsPerGoroutine + i) & 0xFF))

				// Put
				cache.Put(h, RequestPending, types.Hash{})

				// Get
				cache.Get(h)

				// Size
				cache.Size()

				// Delete (every other iteration)
				if i%2 == 0 {
					cache.Delete(h)
				}
			}
		}(g)
	}

	wg.Wait()

	// If we got here without a race condition panic, the test passes.
	// Verify the cache is in a consistent state.
	size := cache.Size()
	if size < 0 {
		t.Fatalf("cache size should be non-negative, got %d", size)
	}
	t.Logf("final cache size after concurrent ops: %d", size)
}

func TestResultCache_PrunePrecision(t *testing.T) {
	ttl := 100 * time.Millisecond
	cache := NewResultCache(100, ttl)

	// Put 3 entries that will expire.
	for i := byte(0); i < 3; i++ {
		h := makeHash(i)
		cache.Put(h, RequestStatus(i), types.Hash{})
	}

	if cache.Size() != 3 {
		t.Fatalf("initial size = %d, want 3", cache.Size())
	}

	// Wait for those 3 entries to expire.
	time.Sleep(150 * time.Millisecond)

	// Put 1 more entry that should survive pruning.
	survivor := makeHash(0xFF)
	cache.Put(survivor, RequestVerified, types.Hash{})

	if cache.Size() != 4 {
		t.Fatalf("size before prune = %d, want 4", cache.Size())
	}

	pruned := cache.Prune()
	if pruned != 3 {
		t.Fatalf("pruned = %d, want 3", pruned)
	}

	if cache.Size() != 1 {
		t.Fatalf("size after prune = %d, want 1", cache.Size())
	}

	// The survivor should still be accessible.
	got, ok := cache.Get(survivor)
	if !ok {
		t.Fatal("survivor entry should still be present after prune")
	}
	if got.Status != RequestVerified {
		t.Fatalf("survivor status = %v, want RequestVerified", got.Status)
	}

	// The expired entries should be gone.
	for i := byte(0); i < 3; i++ {
		_, ok := cache.Get(makeHash(i))
		if ok {
			t.Fatalf("expired entry %d should have been pruned", i)
		}
	}

}
