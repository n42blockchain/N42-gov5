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

package evmsdk

import (
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// CodeCache provides LRU caching of contract bytecodes, keyed by code hash.
type CodeCache struct {
	mu       sync.RWMutex
	cache    map[types.Hash][]byte
	order    []types.Hash // LRU order: most-recently-used at end
	capacity int
}

// NewCodeCache creates a new CodeCache with the given capacity.
// Capacity must be at least 1; values below 1 are clamped to 1.
func NewCodeCache(capacity int) *CodeCache {
	if capacity < 1 {
		capacity = 1
	}
	return &CodeCache{
		cache:    make(map[types.Hash][]byte, capacity),
		order:    make([]types.Hash, 0, capacity),
		capacity: capacity,
	}
}

// Get retrieves bytecode by code hash. Returns (bytecode, true) if found.
func (c *CodeCache) Get(codeHash types.Hash) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	code, ok := c.cache[codeHash]
	if !ok {
		return nil, false
	}

	// Move to end (most-recently-used).
	c.moveToEnd(codeHash)
	return code, true
}

// Insert adds a bytecode to the cache. If the cache is at capacity, the
// least-recently-used entry is evicted.
func (c *CodeCache) Insert(codeHash types.Hash, bytecode []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already present, update and move to end.
	if _, ok := c.cache[codeHash]; ok {
		c.cache[codeHash] = bytecode
		c.moveToEnd(codeHash)
		return
	}

	// Evict LRU if at capacity.
	if len(c.order) >= c.capacity {
		evicted := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, evicted)
	}

	c.cache[codeHash] = bytecode
	c.order = append(c.order, codeHash)
}

// Contains checks if a code hash is present in the cache.
func (c *CodeCache) Contains(codeHash types.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.cache[codeHash]
	return ok
}

// Len returns the number of entries in the cache.
func (c *CodeCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// Remove deletes a single entry from the cache.
func (c *CodeCache) Remove(codeHash types.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.cache[codeHash]; !ok {
		return
	}
	delete(c.cache, codeHash)
	c.removeFromOrder(codeHash)
}

// Clear removes all entries from the cache.
func (c *CodeCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[types.Hash][]byte, c.capacity)
	c.order = c.order[:0]
}

// moveToEnd moves codeHash to the end of the LRU order slice.
// Caller must hold c.mu.
func (c *CodeCache) moveToEnd(codeHash types.Hash) {
	c.removeFromOrder(codeHash)
	c.order = append(c.order, codeHash)
}

// removeFromOrder removes codeHash from the order slice.
// Caller must hold c.mu.
func (c *CodeCache) removeFromOrder(codeHash types.Hash) {
	for i, h := range c.order {
		if h == codeHash {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// HotContractTracker tracks frequently accessed contracts for cache prioritization.
type HotContractTracker struct {
	mu            sync.Mutex
	accessCounts  map[types.Hash]uint64
	blocksTracked uint64
}

// NewHotContractTracker creates a new HotContractTracker.
func NewHotContractTracker() *HotContractTracker {
	return &HotContractTracker{
		accessCounts: make(map[types.Hash]uint64),
	}
}

// RecordBlockAccesses records contract accesses observed in a single block.
// Each hash in the slice increments that contract's access counter.
func (t *HotContractTracker) RecordBlockAccesses(hashes []types.Hash) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, h := range hashes {
		t.accessCounts[h]++
	}
	t.blocksTracked++
}

// TopContracts returns the top n most frequently accessed contract hashes,
// sorted by descending access count.
func (t *HotContractTracker) TopContracts(n int) []types.Hash {
	t.mu.Lock()
	defer t.mu.Unlock()

	type entry struct {
		hash  types.Hash
		count uint64
	}

	entries := make([]entry, 0, len(t.accessCounts))
	for h, c := range t.accessCounts {
		entries = append(entries, entry{hash: h, count: c})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})

	if n > len(entries) {
		n = len(entries)
	}

	result := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		result[i] = entries[i].hash
	}
	return result
}

// Decay multiplies all access counts by the given factor (0.0-1.0) to
// gradually age out stale access patterns. Entries that decay to zero
// are removed.
func (t *HotContractTracker) Decay(factor float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for h, count := range t.accessCounts {
		newCount := uint64(float64(count) * factor)
		if newCount == 0 {
			delete(t.accessCounts, h)
		} else {
			t.accessCounts[h] = newCount
		}
	}
}

// BlocksTracked returns the number of blocks that have been recorded.
func (t *HotContractTracker) BlocksTracked() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.blocksTracked
}
