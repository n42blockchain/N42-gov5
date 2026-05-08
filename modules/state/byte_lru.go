// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// byte_lru.go — concurrent byte-budget LRU cache.
//
// byteLRU is a fixed-byte-budget LRU keyed by a comparable byte-array K.
// Used by the executor's bytecode read cache (codeHash → bytecode). The
// per-entry layout is slot-indexed across pointer-free slabs (the same
// shape as s3FIFO) so GC mark-time scales with the slab count, not the
// live entry count. The previous design with container/list +
// *byteLRUEntry boxing showed up at 305K+305K live heap objects in
// production profiles even after the s3FIFO rewrite.
//
// The value slot remains a []byte slice header (the bytecode itself is
// caller-owned heap memory we don't want to copy); only the per-entry
// container boxes are eliminated.
//
// Hit/miss counters are atomic; the LRU list itself is protected by a
// single mutex (the workload is single-writer + occasional reader from
// the prefetcher, so contention is low and a sync.Map's added complexity
// brings no measurable benefit while making byte accounting awkward).

package state

import (
	"sync"
	"sync/atomic"
)

// byteLRU is a thread-safe slot-indexed LRU bounded by a byte budget.
type byteLRU[K comparable] struct {
	mu        sync.Mutex
	capBytes  int64
	sizeBytes int64

	cap    int // currently allocated slot count
	maxCap int // ceiling derived from capBytes / per-slot estimate

	keys   []K
	values [][]byte // value slice headers; nil for free slots
	costs  []int32
	next   []int32
	prev   []int32

	items map[K]int32

	head, tail int32 // head = MRU, tail = LRU; nilSlot when empty
	free       int32

	hits   atomic.Uint64
	misses atomic.Uint64
}

// byteLRUPerSlotBytes estimates the in-memory footprint of one entry,
// including amortised Go map bucket overhead. Used to derive maxCap
// from the caller-supplied byte budget.
func byteLRUPerSlotBytes[K comparable]() int {
	var zero K
	return keySize(zero) + 24 /*slice header*/ + 4 + 4 + 4 + 50 /*map*/
}

// newByteLRU constructs an LRU with the given byte budget. capBytes <= 0
// disables eviction (the cache is then unbounded — used by tests only).
func newByteLRU[K comparable](capBytes int64) *byteLRU[K] {
	maxCap := initialCacheSlots
	if capBytes > 0 {
		perSlot := int64(byteLRUPerSlotBytes[K]())
		if perSlot <= 0 {
			perSlot = 119
		}
		mc := int(capBytes / perSlot)
		if mc > maxCap {
			maxCap = mc
		}
	}
	cap := initialCacheSlots
	if cap > maxCap {
		cap = maxCap
	}
	c := &byteLRU[K]{
		capBytes: capBytes,
		cap:      cap,
		maxCap:   maxCap,
		keys:     make([]K, cap),
		values:   make([][]byte, cap),
		costs:    make([]int32, cap),
		next:     make([]int32, cap),
		prev:     make([]int32, cap),
		items:    make(map[K]int32),
		head:     nilSlot,
		tail:     nilSlot,
		free:     0,
	}
	for i := int32(0); i < int32(cap)-1; i++ {
		c.next[i] = i + 1
	}
	c.next[cap-1] = nilSlot
	return c
}

// growLocked doubles cap up to maxCap when the free list is exhausted.
// Caller must hold c.mu.
func (c *byteLRU[K]) growLocked() bool {
	if c.cap >= c.maxCap {
		return false
	}
	newCap := c.cap * 2
	if newCap > c.maxCap {
		newCap = c.maxCap
	}
	delta := newCap - c.cap
	c.keys = append(c.keys, make([]K, delta)...)
	c.values = append(c.values, make([][]byte, delta)...)
	c.costs = append(c.costs, make([]int32, delta)...)
	c.next = append(c.next, make([]int32, delta)...)
	c.prev = append(c.prev, make([]int32, delta)...)
	for i := int32(c.cap); i < int32(newCap)-1; i++ {
		c.next[i] = i + 1
	}
	c.next[newCap-1] = c.free
	c.free = int32(c.cap)
	c.cap = newCap
	return true
}

func (c *byteLRU[K]) allocSlot() int32 {
	if c.free == nilSlot {
		return nilSlot
	}
	idx := c.free
	c.free = c.next[idx]
	return idx
}

func (c *byteLRU[K]) freeSlot(idx int32) {
	c.values[idx] = nil // release bytecode reference for GC
	c.prev[idx] = nilSlot
	c.next[idx] = c.free
	c.free = idx
}

// listPushFront makes idx the new head (MRU).
func (c *byteLRU[K]) listPushFront(idx int32) {
	c.prev[idx] = nilSlot
	c.next[idx] = c.head
	if c.head != nilSlot {
		c.prev[c.head] = idx
	} else {
		c.tail = idx
	}
	c.head = idx
}

// listRemove unlinks idx from the LRU list.
func (c *byteLRU[K]) listRemove(idx int32) {
	p, n := c.prev[idx], c.next[idx]
	if p != nilSlot {
		c.next[p] = n
	} else {
		c.head = n
	}
	if n != nilSlot {
		c.prev[n] = p
	} else {
		c.tail = p
	}
	c.prev[idx] = nilSlot
	c.next[idx] = nilSlot
}

// Get returns the value for key and promotes it to MRU. Returned slice
// aliases caller-supplied bytecode bytes; callers must not mutate.
func (c *byteLRU[K]) Get(key K) (value []byte, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	if idx != c.head {
		c.listRemove(idx)
		c.listPushFront(idx)
	}
	c.hits.Add(1)
	return c.values[idx], true
}

// Put inserts or replaces an entry. cost is the byte budget impact.
func (c *byteLRU[K]) Put(key K, value []byte, cost int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, value, cost)
	c.evictLocked()
}

// PutBatch inserts/replaces a batch of entries under a single lock.
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

// PutIfAbsent inserts only when key is not already present.
func (c *byteLRU[K]) PutIfAbsent(key K, value []byte, cost int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return false
	}
	c.putLocked(key, value, cost)
	c.evictLocked()
	return true
}

func (c *byteLRU[K]) putLocked(key K, value []byte, cost int) {
	if idx, ok := c.items[key]; ok {
		c.sizeBytes += int64(cost) - int64(c.costs[idx])
		c.values[idx] = value
		c.costs[idx] = int32(cost)
		if idx != c.head {
			c.listRemove(idx)
			c.listPushFront(idx)
		}
		return
	}
	idx := c.allocSlot()
	if idx == nilSlot {
		if !c.growLocked() {
			return
		}
		idx = c.allocSlot()
		if idx == nilSlot {
			return
		}
	}
	c.keys[idx] = key
	c.values[idx] = value
	c.costs[idx] = int32(cost)
	c.listPushFront(idx)
	c.items[key] = idx
	c.sizeBytes += int64(cost)
}

// Delete removes a key from the cache. Returns true if it existed.
func (c *byteLRU[K]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleteLocked(key)
}

// DeleteBatch removes a batch of keys under a single lock.
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

func (c *byteLRU[K]) deleteLocked(key K) bool {
	idx, ok := c.items[key]
	if !ok {
		return false
	}
	c.listRemove(idx)
	delete(c.items, key)
	c.sizeBytes -= int64(c.costs[idx])
	c.freeSlot(idx)
	return true
}

// Reset drops all entries.
func (c *byteLRU[K]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.values {
		c.values[i] = nil
	}
	for i := int32(0); i < int32(c.cap)-1; i++ {
		c.next[i] = i + 1
		c.prev[i] = nilSlot
	}
	c.next[c.cap-1] = nilSlot
	c.prev[c.cap-1] = nilSlot
	c.free = 0
	c.head, c.tail = nilSlot, nilSlot
	c.sizeBytes = 0
	c.items = make(map[K]int32)
}

// Stats returns (hits, misses, currentBytes, entries).
func (c *byteLRU[K]) Stats() (hits, misses uint64, bytes int64, entries int) {
	c.mu.Lock()
	live := len(c.items)
	bytes = c.sizeBytes
	c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), bytes, live
}

// ResetStats clears hit/miss counters without touching cache contents.
func (c *byteLRU[K]) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
}

// evictLocked drops entries from the tail (LRU) until sizeBytes fits.
// Caller must hold c.mu. capBytes <= 0 disables eviction.
func (c *byteLRU[K]) evictLocked() {
	if c.capBytes <= 0 {
		return
	}
	for c.sizeBytes > c.capBytes {
		idx := c.tail
		if idx == nilSlot {
			c.sizeBytes = 0
			return
		}
		c.listRemove(idx)
		delete(c.items, c.keys[idx])
		c.sizeBytes -= int64(c.costs[idx])
		c.freeSlot(idx)
	}
}
