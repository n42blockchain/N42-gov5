// Copyright 2022-2026 The N42 Authors
package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBytecodeCacheGetPutEvict(t *testing.T) {
	c := NewBytecodeCache(bytecodeShards * 2) // 2 entries/shard

	var h1, h2, h3 types.Hash
	h1[0] = 5
	h1[1] = 1
	h2[0] = 5
	h2[1] = 2
	h3[0] = 5
	h3[1] = 3

	if _, ok := c.Get(h1); ok {
		t.Fatalf("expected miss on empty cache")
	}

	c.Put(h1, []byte{1, 2, 3})
	c.Put(h2, []byte{4, 5, 6})

	if code, ok := c.Get(h1); !ok || len(code) != 3 {
		t.Fatalf("expected hit for h1, got %v %v", code, ok)
	}

	if c.Size() != 2 {
		t.Fatalf("expected size 2, got %d", c.Size())
	}

	// Re-putting an existing key should just move it to front, not duplicate.
	c.Put(h1, []byte{9, 9, 9})
	if c.Size() != 2 {
		t.Fatalf("expected size unchanged after re-put, got %d", c.Size())
	}

	// Adding a third entry to a 2-capacity shard evicts the LRU back entry.
	c.Put(h3, []byte{7, 8, 9})
	if c.Size() != 2 {
		t.Fatalf("expected size still 2 after eviction, got %d", c.Size())
	}
	if _, ok := c.Get(h3); !ok {
		t.Fatalf("expected h3 present after insert")
	}
}

func TestBytecodeCacheMinCapacityPerShard(t *testing.T) {
	// capacity smaller than shard count still gives every shard capacity 1.
	c := NewBytecodeCache(1)
	var h types.Hash
	h[0] = 10
	c.Put(h, []byte{1})
	if code, ok := c.Get(h); !ok || len(code) != 1 {
		t.Fatalf("expected hit, got %v %v", code, ok)
	}
}

func TestBytecodeCacheDistinctShards(t *testing.T) {
	c := NewBytecodeCache(bytecodeShards)
	var a, b types.Hash
	a[0] = 1
	b[0] = 2
	c.Put(a, []byte{1})
	c.Put(b, []byte{2})
	if c.Size() != 2 {
		t.Fatalf("expected 2 entries across distinct shards, got %d", c.Size())
	}
}
