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

package vm

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestCodeAnalysisCache_GetPut(t *testing.T) {
	cache := NewCodeAnalysisCache(16)

	h := types.HexToHash("0x0102030405060708091011121314151617181920212223242526272829303132")
	analysis := []uint64{1, 2, 3, 4}

	// Miss before put.
	if _, ok := cache.Get(h); ok {
		t.Fatal("expected cache miss before Put")
	}

	cache.Put(h, analysis)

	got, ok := cache.Get(h)
	if !ok {
		t.Fatal("expected cache hit after Put")
	}
	if len(got) != len(analysis) {
		t.Fatalf("analysis length = %d, want %d", len(got), len(analysis))
	}
	for i := range analysis {
		if got[i] != analysis[i] {
			t.Fatalf("analysis[%d] = %d, want %d", i, got[i], analysis[i])
		}
	}

	if cache.Size() != 1 {
		t.Fatalf("cache size = %d, want 1", cache.Size())
	}

	// Verify returned slice is a copy (mutating it doesn't affect cache).
	got[0] = 999
	got2, _ := cache.Get(h)
	if got2[0] == 999 {
		t.Fatal("Get returned a reference, not a copy")
	}
}

func TestCodeAnalysisCache_LRUEviction(t *testing.T) {
	capacity := 3
	cache := NewCodeAnalysisCache(capacity)

	hashes := make([]types.Hash, 5)
	for i := range hashes {
		hashes[i][0] = byte(i + 1)
	}

	// Fill cache to capacity.
	for i := 0; i < capacity; i++ {
		cache.Put(hashes[i], []uint64{uint64(i)})
	}
	if cache.Size() != capacity {
		t.Fatalf("cache size = %d, want %d", cache.Size(), capacity)
	}

	// Add one more, should evict the LRU (hashes[0]).
	cache.Put(hashes[3], []uint64{3})
	if cache.Size() != capacity {
		t.Fatalf("cache size after eviction = %d, want %d", cache.Size(), capacity)
	}

	// hashes[0] should be evicted.
	if _, ok := cache.Get(hashes[0]); ok {
		t.Fatal("expected hashes[0] to be evicted")
	}

	// hashes[1], hashes[2], hashes[3] should still be present.
	for _, i := range []int{1, 2, 3} {
		if _, ok := cache.Get(hashes[i]); !ok {
			t.Fatalf("expected hashes[%d] to be present", i)
		}
	}

	// Access hashes[1] to promote it, then add two more to evict hashes[2] then hashes[3].
	cache.Get(hashes[1]) // promote hashes[1]
	cache.Put(hashes[4], []uint64{4})

	// hashes[2] should be evicted now (was LRU after hashes[1] was promoted).
	if _, ok := cache.Get(hashes[2]); ok {
		t.Fatal("expected hashes[2] to be evicted after promotion of hashes[1]")
	}
	if _, ok := cache.Get(hashes[1]); !ok {
		t.Fatal("expected hashes[1] to survive after promotion")
	}
}

func TestCodeAnalysisCache_Concurrent(t *testing.T) {
	cache := NewCodeAnalysisCache(64)
	var wg sync.WaitGroup
	goroutines := 10
	opsPerGoroutine := 100

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				var h types.Hash
				h[0] = byte(id)
				h[1] = byte(i)
				analysis := []uint64{uint64(id*1000 + i)}
				cache.Put(h, analysis)

				got, ok := cache.Get(h)
				if ok && len(got) == 1 && got[0] != uint64(id*1000+i) {
					t.Errorf("goroutine %d: unexpected value %d for key (%d,%d)", id, got[0], id, i)
				}
			}
		}(g)
	}
	wg.Wait()

	if cache.Size() > 64 {
		t.Fatalf("cache size %d exceeds capacity 64", cache.Size())
	}
}

func TestCodeAnalysisCache_Nil(t *testing.T) {
	// Verify that GlobalCodeAnalysisCache=nil doesn't panic in isCode.
	old := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = nil
	defer func() { GlobalCodeAnalysisCache = old }()

	c := &Contract{
		Code:     []byte{byte(JUMPDEST), byte(PUSH1), 0x01, byte(JUMPDEST)},
		CodeHash: types.HexToHash("0xdeadbeef"),
		jumpdests: make(map[types.Hash][]uint64),
	}

	// Should not panic and should work correctly.
	result := c.isCode(0)
	if !result {
		t.Fatal("expected position 0 (JUMPDEST) to be code")
	}
	result = c.isCode(3)
	if !result {
		t.Fatal("expected position 3 (JUMPDEST) to be code")
	}
}
