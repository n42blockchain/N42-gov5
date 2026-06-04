// Copyright 2022-2026 The N42 Authors
// Process-wide bytecode cache for witness replay. Code is content-
// addressed (key = keccak256(code)) and immutable — once loaded by any
// worker, all workers can share the slice.
//
// SHARDED (256 shards by codeHash[0]) so the 32 replay workers don't all
// contend on one RWMutex: profiling showed a single-mutex Get was ~5% of CPU
// (the RWMutex's internal atomic reader-count cache-line bounced across cores,
// plus the resulting thread parking). Sharding spreads it across 256 atomics →
// effectively contention-free. Per-shard LRU approximates a global LRU.

package ethel

import (
	"container/list"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

const bytecodeShards = 256 // routed by codeHash[0]; power-of-two not required

type bytecodeShard struct {
	mu       sync.RWMutex
	cache    map[types.Hash]*list.Element
	lru      *list.List
	capacity int
}

// BytecodeCache is a thread-safe sharded LRU keyed by codeHash. Get takes only
// the relevant shard's read lock; Put takes that shard's write lock and may
// evict within the shard.
type BytecodeCache struct {
	shards [bytecodeShards]bytecodeShard
}

type bytecodeEntry struct {
	hash types.Hash
	code []byte
}

// GlobalBytecodeCache is the process-wide cache. nil = disabled.
var GlobalBytecodeCache *BytecodeCache

// NewBytecodeCache creates a sharded LRU with the given TOTAL capacity (split
// evenly across shards, min 1 entry/shard).
func NewBytecodeCache(capacity int) *BytecodeCache {
	per := capacity / bytecodeShards
	if per < 1 {
		per = 1
	}
	c := &BytecodeCache{}
	for i := range c.shards {
		c.shards[i] = bytecodeShard{
			cache:    make(map[types.Hash]*list.Element, per),
			lru:      list.New(),
			capacity: per,
		}
	}
	return c
}

func (c *BytecodeCache) shard(codeHash types.Hash) *bytecodeShard {
	return &c.shards[codeHash[0]]
}

// Get returns the cached bytecode for codeHash, or (nil, false). Non-promoting
// on hit (keeps the hot path a single shard read-lock + map lookup).
func (c *BytecodeCache) Get(codeHash types.Hash) ([]byte, bool) {
	s := c.shard(codeHash)
	s.mu.RLock()
	elem, ok := s.cache[codeHash]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	code := elem.Value.(*bytecodeEntry).code
	s.mu.RUnlock()
	return code, true
}

// Put stores the bytecode. The cached slice is the same backing array we store
// — callers MUST treat it as immutable (it comes from a content-addressed
// table, so this is the natural contract).
func (c *BytecodeCache) Put(codeHash types.Hash, code []byte) {
	s := c.shard(codeHash)
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.cache[codeHash]; ok {
		s.lru.MoveToFront(elem)
		return
	}
	for s.lru.Len() >= s.capacity {
		back := s.lru.Back()
		if back == nil {
			break
		}
		evicted := s.lru.Remove(back).(*bytecodeEntry)
		delete(s.cache, evicted.hash)
	}
	entry := &bytecodeEntry{hash: codeHash, code: code}
	s.cache[codeHash] = s.lru.PushFront(entry)
}

// Size returns the total number of cached entries across all shards.
func (c *BytecodeCache) Size() int {
	n := 0
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.RLock()
		n += s.lru.Len()
		s.mu.RUnlock()
	}
	return n
}
