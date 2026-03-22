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
	"container/list"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// CodeAnalysisCache caches contract bytecode analysis results (bitvec) across
// blocks, keyed by codeHash. This avoids re-running O(n) codeBitmap() analysis
// for contracts already seen in previous blocks. Inspired by Aptos AIP-107.
//
// Thread-safe for concurrent use by parallel EVM instances (Block-STM).
type CodeAnalysisCache struct {
	mu       sync.RWMutex
	cache    map[types.Hash]*list.Element
	lru      *list.List
	capacity int
}

type codeEntry struct {
	hash     types.Hash
	analysis []uint64
}

// GlobalCodeAnalysisCache is the process-wide code analysis cache.
// Set during node initialization. May be nil (cache disabled).
var GlobalCodeAnalysisCache *CodeAnalysisCache

// NewCodeAnalysisCache creates a new code analysis LRU cache with the given capacity.
func NewCodeAnalysisCache(capacity int) *CodeAnalysisCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &CodeAnalysisCache{
		cache:    make(map[types.Hash]*list.Element, capacity),
		lru:      list.New(),
		capacity: capacity,
	}
}

// Get retrieves a cached code analysis by codeHash.
// Returns the bitvec slice and true if found, or nil and false if not.
//
// The returned slice MUST NOT be mutated by callers. The bitvec is immutable
// after creation by codeBitmap() and is shared across concurrent readers.
func (c *CodeAnalysisCache) Get(codeHash types.Hash) ([]uint64, bool) {
	c.mu.RLock()
	elem, ok := c.cache[codeHash]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	entry := elem.Value.(*codeEntry)
	// Return the slice directly — bitvec is immutable after creation.
	// All callers (isCodeFromAnalysis) only read; no mutation ever occurs.
	result := entry.analysis
	c.mu.RUnlock()

	// Promote to front under write lock.
	c.mu.Lock()
	// Re-check in case of eviction between RUnlock and Lock.
	if elem2, still := c.cache[codeHash]; still {
		c.lru.MoveToFront(elem2)
	}
	c.mu.Unlock()

	return result, true
}

// Put stores a code analysis result in the cache. If the cache is at capacity,
// the least recently used entry is evicted.
func (c *CodeAnalysisCache) Put(codeHash types.Hash, analysis []uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already present, promote and update.
	if elem, ok := c.cache[codeHash]; ok {
		c.lru.MoveToFront(elem)
		entry := elem.Value.(*codeEntry)
		entry.analysis = make([]uint64, len(analysis))
		copy(entry.analysis, analysis)
		return
	}

	// Evict LRU if at capacity.
	for c.lru.Len() >= c.capacity {
		back := c.lru.Back()
		if back == nil {
			break
		}
		evicted := c.lru.Remove(back).(*codeEntry)
		delete(c.cache, evicted.hash)
	}

	// Store a copy to avoid the caller mutating the cached data.
	stored := make([]uint64, len(analysis))
	copy(stored, analysis)

	entry := &codeEntry{hash: codeHash, analysis: stored}
	elem := c.lru.PushFront(entry)
	c.cache[codeHash] = elem
}

// Size returns the number of entries in the cache.
func (c *CodeAnalysisCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}
