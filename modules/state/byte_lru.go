// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// byte_lru.go — concurrent byte-budget LRU cache.
//
// byteLRU is a fixed-byte-budget LRU keyed by a comparable type. Each entry
// carries an explicit size cost (set by the caller) and the cache evicts
// least-recently-used entries until the total cost is below the configured
// budget. Hit/miss counters are atomic; the LRU list itself is protected by
// a single mutex (the workload is single-writer + occasional reader from
// the prefetcher, so contention is low and a sync.Map's added complexity
// brings no measurable benefit while making byte accounting awkward).
//
// The cache is intended for the executor's state read cache. Three
// instances are created: one for accounts, one for individual storage
// slots, one for contract code. Each gets its own byte budget so a hot
// storage workload can't starve the account cache.

package state

import (
	"container/list"
	"sync"
	"sync/atomic"
)

// byteLRUEntry holds one cache entry: the key, the cached value bytes, and
// the byte cost (used to drive eviction). The value is opaque to the LRU —
// callers wrap typed values in []byte at the boundary.
type byteLRUEntry[K comparable] struct {
	key   K
	value []byte
	// cost is the byte size we charge against the budget. It can include
	// estimated overhead for the LRU list node + map entry so callers can
	// reflect realistic memory pressure.
	cost int
}

// byteLRU is a thread-safe LRU bounded by a byte budget.
type byteLRU[K comparable] struct {
	mu        sync.Mutex
	capBytes  int64
	sizeBytes int64
	items     map[K]*list.Element
	lru       *list.List // front = MRU, back = LRU

	hits   atomic.Uint64
	misses atomic.Uint64
}

// newByteLRU constructs an LRU with the given byte budget. capBytes <= 0
// disables eviction (the cache is then unbounded — used by tests only).
func newByteLRU[K comparable](capBytes int64) *byteLRU[K] {
	return &byteLRU[K]{
		capBytes: capBytes,
		items:    make(map[K]*list.Element),
		lru:      list.New(),
	}
}

// Get returns the value for key and promotes it to MRU. The returned slice
// is the cached storage; callers must not mutate it. nilOK distinguishes
// "key was cached as nil/absent" from "cache miss" — the second return
// reports whether the key was present at all.
func (c *byteLRU[K]) Get(key K) (value []byte, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.lru.MoveToFront(elem)
	c.hits.Add(1)
	return elem.Value.(*byteLRUEntry[K]).value, true
}

// Put inserts or replaces an entry. cost is the byte budget impact (see
// byteLRUEntry.cost). Replacing an existing key updates the cost delta.
func (c *byteLRU[K]) Put(key K, value []byte, cost int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, value, cost)
	c.evictLocked()
}

// PutBatch inserts/replaces a batch of entries under a single mutex
// acquisition. Used by RefreshLRUForSnapshot to repopulate the cache
// with the just-flushed working set after a background commit — taking
// the lock once avoids cache-line ping-pong with concurrent Get() calls
// and amortises the eviction sweep across the whole batch.
//
// keys, values and costs must have the same length; the value at index
// i is associated with key[i] and charged costs[i].
func (c *byteLRU[K]) PutBatch(keys []K, values [][]byte, costs []int) {
	if len(keys) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, k := range keys {
		c.putLocked(k, values[i], costs[i])
	}
	c.evictLocked()
}

// putLocked is the unsynchronised core of Put / PutBatch.
func (c *byteLRU[K]) putLocked(key K, value []byte, cost int) {
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*byteLRUEntry[K])
		c.sizeBytes += int64(cost - entry.cost)
		entry.value = value
		entry.cost = cost
		c.lru.MoveToFront(elem)
	} else {
		entry := &byteLRUEntry[K]{key: key, value: value, cost: cost}
		elem := c.lru.PushFront(entry)
		c.items[key] = elem
		c.sizeBytes += int64(cost)
	}
}

// Delete removes a key from the cache. Returns true if the key existed.
func (c *byteLRU[K]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleteLocked(key)
}

// DeleteBatch removes a batch of keys under a single mutex acquisition.
// Used by InvalidateLRUForSnapshot, which evicts the entire dirty set
// after a background flush — taking the lock once instead of N times
// avoids cache-line ping-pong with concurrent reader Get() calls.
func (c *byteLRU[K]) DeleteBatch(keys []K) {
	if len(keys) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		c.deleteLocked(k)
	}
}

// deleteLocked removes a key. Caller must hold c.mu.
func (c *byteLRU[K]) deleteLocked(key K) bool {
	elem, ok := c.items[key]
	if !ok {
		return false
	}
	entry := elem.Value.(*byteLRUEntry[K])
	c.lru.Remove(elem)
	delete(c.items, key)
	c.sizeBytes -= int64(entry.cost)
	return true
}

// Reset drops all entries.
func (c *byteLRU[K]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[K]*list.Element)
	c.lru = list.New()
	c.sizeBytes = 0
}

// Stats returns (hits, misses, currentBytes, entries).
func (c *byteLRU[K]) Stats() (hits, misses uint64, bytes int64, entries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), c.sizeBytes, len(c.items)
}

// ResetStats clears hit/miss counters without touching cache contents.
func (c *byteLRU[K]) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
}

// evictLocked drops entries from the LRU end until sizeBytes <= capBytes.
// Caller must hold c.mu. capBytes <= 0 disables eviction.
func (c *byteLRU[K]) evictLocked() {
	if c.capBytes <= 0 {
		return
	}
	for c.sizeBytes > c.capBytes {
		tail := c.lru.Back()
		if tail == nil {
			c.sizeBytes = 0
			return
		}
		entry := tail.Value.(*byteLRUEntry[K])
		c.lru.Remove(tail)
		delete(c.items, entry.key)
		c.sizeBytes -= int64(entry.cost)
	}
}
