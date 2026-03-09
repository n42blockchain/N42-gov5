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

package layered

import (
	"fmt"
	"sync"
	"testing"
)

func TestShardedCache_BasicGetPut(t *testing.T) {
	c := NewShardedCache(16, 100)

	// Miss.
	v, ok := c.Get("Account", []byte("addr1"))
	if ok {
		t.Fatal("expected miss on empty cache")
	}
	if v != nil {
		t.Fatalf("expected nil, got %v", v)
	}

	// Put and hit.
	c.Put("Account", []byte("addr1"), []byte("value1"))
	v, ok = c.Get("Account", []byte("addr1"))
	if !ok {
		t.Fatal("expected hit after put")
	}
	if string(v) != "value1" {
		t.Fatalf("expected value1, got %s", v)
	}
}

func TestShardedCache_NegativeCache(t *testing.T) {
	c := NewShardedCache(16, 100)

	// Store nil value (negative cache for "not found").
	c.Put("Account", []byte("missing"), nil)
	v, ok := c.Get("Account", []byte("missing"))
	if !ok {
		t.Fatal("expected hit for negative cache")
	}
	if v != nil {
		t.Fatal("expected nil value for negative cache")
	}
}

func TestShardedCache_Delete(t *testing.T) {
	c := NewShardedCache(16, 100)
	c.Put("Account", []byte("addr1"), []byte("v"))
	c.Delete("Account", []byte("addr1"))

	_, ok := c.Get("Account", []byte("addr1"))
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestShardedCache_Eviction(t *testing.T) {
	c := NewShardedCache(1, 3) // 1 shard, capacity 3

	c.Put("Account", []byte("a"), []byte("1"))
	c.Put("Account", []byte("b"), []byte("2"))
	c.Put("Account", []byte("c"), []byte("3"))
	// Should be full now. Adding one more evicts oldest.
	c.Put("Account", []byte("d"), []byte("4"))

	// "a" should have been evicted (LRU).
	_, ok := c.Get("Account", []byte("a"))
	if ok {
		t.Fatal("expected 'a' to be evicted")
	}

	// "d" should be present.
	v, ok := c.Get("Account", []byte("d"))
	if !ok || string(v) != "4" {
		t.Fatalf("expected 'd'='4', got ok=%v v=%s", ok, v)
	}
}

func TestShardedCache_Update(t *testing.T) {
	c := NewShardedCache(16, 100)
	c.Put("Account", []byte("k"), []byte("old"))
	c.Put("Account", []byte("k"), []byte("new"))

	v, ok := c.Get("Account", []byte("k"))
	if !ok || string(v) != "new" {
		t.Fatalf("expected 'new', got ok=%v v=%s", ok, v)
	}
}

func TestShardedCache_TableIsolation(t *testing.T) {
	c := NewShardedCache(16, 100)
	c.Put("Account", []byte("k"), []byte("account_val"))
	c.Put("Storage", []byte("k"), []byte("storage_val"))

	v1, _ := c.Get("Account", []byte("k"))
	v2, _ := c.Get("Storage", []byte("k"))

	if string(v1) != "account_val" {
		t.Fatalf("Account table: expected account_val, got %s", v1)
	}
	if string(v2) != "storage_val" {
		t.Fatalf("Storage table: expected storage_val, got %s", v2)
	}
}

func TestShardedCache_Stats(t *testing.T) {
	c := NewShardedCache(16, 100)

	c.Put("Account", []byte("k"), []byte("v"))
	c.Get("Account", []byte("k"))     // hit
	c.Get("Account", []byte("miss"))  // miss

	hits, misses, entries := c.Stats()
	if hits != 1 {
		t.Errorf("hits: want 1, got %d", hits)
	}
	if misses != 1 {
		t.Errorf("misses: want 1, got %d", misses)
	}
	if entries != 1 {
		t.Errorf("entries: want 1, got %d", entries)
	}
}

func TestShardedCache_Clear(t *testing.T) {
	c := NewShardedCache(16, 100)
	for i := 0; i < 50; i++ {
		c.Put("Account", []byte(fmt.Sprintf("k%d", i)), []byte("v"))
	}
	c.Clear()

	_, _, entries := c.Stats()
	if entries != 0 {
		t.Fatalf("entries after clear: want 0, got %d", entries)
	}
}

func TestShardedCache_ConcurrentAccess(t *testing.T) {
	c := NewShardedCache(64, 1000)
	var wg sync.WaitGroup
	n := 100

	// Concurrent writers.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("key-%d", i))
			val := []byte(fmt.Sprintf("val-%d", i))
			c.Put("Account", key, val)
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("key-%d", i))
			c.Get("Account", key)
		}(i)
	}

	wg.Wait()

	// All entries should be present.
	_, _, entries := c.Stats()
	if entries != n {
		t.Errorf("entries: want %d, got %d", n, entries)
	}
}

func TestShardedCache_ValueCopyIsolation(t *testing.T) {
	c := NewShardedCache(16, 100)
	original := []byte("original")
	c.Put("Account", []byte("k"), original)

	// Modify the original slice — cache should not be affected.
	original[0] = 'X'

	v, ok := c.Get("Account", []byte("k"))
	if !ok {
		t.Fatal("expected hit")
	}
	if string(v) != "original" {
		t.Fatalf("cache value was mutated: got %s", v)
	}
}
