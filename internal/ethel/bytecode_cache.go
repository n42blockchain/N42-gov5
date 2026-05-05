// Copyright 2022-2026 The N42 Authors
// Process-wide bytecode cache for witness replay. Code is content-
// addressed (key = keccak256(code)) and immutable — once loaded by any
// worker, all workers can share the slice. CGo to MDBX accounted for
// ~7% of replay CPU before this cache (every contract call ran
// ReadAccountCode → MDBX GetOne → cgocall).

package ethel

import (
	"container/list"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// BytecodeCache is a thread-safe LRU keyed by codeHash. Lock-free Get
// fast path mirrors vm.CodeAnalysisCache (same parallelism contention
// concern); Put takes a write lock and may evict.
type BytecodeCache struct {
	mu       sync.RWMutex
	cache    map[types.Hash]*list.Element
	lru      *list.List
	capacity int
}

type bytecodeEntry struct {
	hash types.Hash
	code []byte
}

// GlobalBytecodeCache is the process-wide cache. nil = disabled.
var GlobalBytecodeCache *BytecodeCache

// NewBytecodeCache creates an LRU with the given capacity.
func NewBytecodeCache(capacity int) *BytecodeCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &BytecodeCache{
		cache:    make(map[types.Hash]*list.Element, capacity),
		lru:      list.New(),
		capacity: capacity,
	}
}

// Get returns the cached bytecode for codeHash, or (nil, false).
// Non-promoting on hit (lock-free fast path); see vm.CodeAnalysisCache
// for the rationale.
func (c *BytecodeCache) Get(codeHash types.Hash) ([]byte, bool) {
	c.mu.RLock()
	elem, ok := c.cache[codeHash]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	code := elem.Value.(*bytecodeEntry).code
	c.mu.RUnlock()
	return code, true
}

// Put stores the bytecode. Cached slice is the same backing array we
// store — callers MUST treat it as immutable (it's already pulled
// from a content-addressed table, so this is the natural contract).
func (c *BytecodeCache) Put(codeHash types.Hash, code []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cache[codeHash]; ok {
		c.lru.MoveToFront(elem)
		return
	}

	for c.lru.Len() >= c.capacity {
		back := c.lru.Back()
		if back == nil {
			break
		}
		evicted := c.lru.Remove(back).(*bytecodeEntry)
		delete(c.cache, evicted.hash)
	}

	entry := &bytecodeEntry{hash: codeHash, code: code}
	elem := c.lru.PushFront(entry)
	c.cache[codeHash] = elem
}

// Size returns the number of cached entries.
func (c *BytecodeCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}
