// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// s3fifo.go — Simple Scalable Scan-resistant FIFO cache.
//
// Reference: Yang, Jiacheng et al. "FIFO queues are all you need for
// cache eviction." SOSP 2023. https://dl.acm.org/doi/10.1145/3600006.3613147
//
// Three queues: small (S, 10%), main (M, 90%), ghost (G, key-only).
// New Put lands in S; one-touch entries are evicted from S to G
// without polluting M, giving the policy natural scan resistance —
// critical for ETH storage where each commit-interval RefreshLRU
// dumps 700K+ one-touch writes (NFT mints, ERC20 approvals).
//
// Eviction:
//   - S tail with freq>0 → promote to M (it has earned a place)
//   - S tail with freq==0 → drop key into G
//   - M tail with freq>0 → freq--, requeue at head (second chance)
//   - M tail with freq==0 → drop key into G
//
// Re-admission:
//   - Put for a key in G → insert directly into M (it was hot enough
//     to come back), bypass S
//
// Interface: identical to byteLRU so the read-cache field types in
// PlainStateBuffer can be swapped without touching the Reader hot path.

package state

import (
	"container/list"
	"sync"
	"sync/atomic"
)

// s3Entry is a cache entry stored in either the S or M FIFO queue.
type s3Entry[K comparable] struct {
	key    K
	value  []byte
	cost   int
	freq   uint8 // 0..3, increments on access, decrements on second-chance pass
	inMain bool  // false=S queue, true=M queue
}

// s3FIFO is a cost-bounded scan-resistant cache.
type s3FIFO[K comparable] struct {
	mu sync.Mutex

	capBytes int64
	sCap     int64 // small queue capacity (≈10% of capBytes)
	sBytes   int64
	mBytes   int64

	items  map[K]*list.Element // entries currently in S or M
	ghosts map[K]*list.Element // ghost-set membership; element holds the key

	sList *list.List // small FIFO (front=newest)
	mList *list.List // main FIFO
	gList *list.List // ghost FIFO

	maxGhost int

	hits   atomic.Uint64
	misses atomic.Uint64
}

// newS3FIFO constructs a cache with the given byte budget. The S
// queue gets ~10% of the budget; ghost is bounded to ~100K entries
// per GB of capacity (so ghost overhead stays below ~5% of cache).
func newS3FIFO[K comparable](capBytes int64) *s3FIFO[K] {
	sCap := capBytes / 10
	if sCap < 1024 {
		sCap = capBytes / 4 // tiny caches: bump S share
	}
	maxGhost := int(100_000 * capBytes / (1 << 30))
	if maxGhost < 16384 {
		maxGhost = 16384
	}
	return &s3FIFO[K]{
		capBytes: capBytes,
		sCap:     sCap,
		items:    make(map[K]*list.Element),
		ghosts:   make(map[K]*list.Element),
		sList:    list.New(),
		mList:    list.New(),
		gList:    list.New(),
		maxGhost: maxGhost,
	}
}

// Get returns the value for key and bumps its frequency. The returned
// slice is the cached storage; callers must not mutate it.
func (c *s3FIFO[K]) Get(key K) (value []byte, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	entry := elem.Value.(*s3Entry[K])
	if entry.freq < 3 {
		entry.freq++
	}
	c.hits.Add(1)
	return entry.value, true
}

// Put inserts or updates key. New keys land in S; keys present in
// the ghost set get re-admitted directly to M.
func (c *s3FIFO[K]) Put(key K, value []byte, cost int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, value, cost)
	c.evictLocked()
}

// PutBatch is the batch form used by RefreshLRUForSnapshot. Single
// mutex acquisition + amortised eviction sweep at the end.
func (c *s3FIFO[K]) PutBatch(keys []K, values [][]byte, costs []int) {
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

func (c *s3FIFO[K]) putLocked(key K, value []byte, cost int) {
	// Update existing entry in S or M.
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*s3Entry[K])
		if entry.inMain {
			c.mBytes += int64(cost - entry.cost)
		} else {
			c.sBytes += int64(cost - entry.cost)
		}
		entry.value = value
		entry.cost = cost
		if entry.freq < 3 {
			entry.freq++
		}
		return
	}
	// Re-admission from ghost: this key was here recently enough that
	// its history is still in G — skip the S audition and go straight
	// to M.
	if gElem, ok := c.ghosts[key]; ok {
		c.gList.Remove(gElem)
		delete(c.ghosts, key)
		entry := &s3Entry[K]{key: key, value: value, cost: cost, freq: 0, inMain: true}
		elem := c.mList.PushFront(entry)
		c.items[key] = elem
		c.mBytes += int64(cost)
		return
	}
	// Fresh key → small queue.
	entry := &s3Entry[K]{key: key, value: value, cost: cost, freq: 0, inMain: false}
	elem := c.sList.PushFront(entry)
	c.items[key] = elem
	c.sBytes += int64(cost)
}

// evictLocked drives both queues down to capBytes. Prefers evicting
// from S when S exceeds its share; otherwise pushes M's tail through
// the second-chance loop.
func (c *s3FIFO[K]) evictLocked() {
	if c.capBytes <= 0 {
		return
	}
	for c.sBytes+c.mBytes > c.capBytes {
		if c.sBytes > c.sCap || c.mList.Len() == 0 {
			if !c.evictSLocked() && !c.evictMLocked() {
				return
			}
		} else {
			if !c.evictMLocked() && !c.evictSLocked() {
				return
			}
		}
	}
}

// evictSLocked pops S tail. Promotes to M if it earned at least one
// access; otherwise drops the key into G.
func (c *s3FIFO[K]) evictSLocked() bool {
	tail := c.sList.Back()
	if tail == nil {
		return false
	}
	entry := tail.Value.(*s3Entry[K])
	c.sList.Remove(tail)
	c.sBytes -= int64(entry.cost)
	if entry.freq > 0 {
		entry.freq = 0
		entry.inMain = true
		elem := c.mList.PushFront(entry)
		c.items[entry.key] = elem
		c.mBytes += int64(entry.cost)
		return true
	}
	delete(c.items, entry.key)
	c.addGhostLocked(entry.key)
	return true
}

// evictMLocked walks M's tail with the second-chance rule. Bounded
// to 32 demotions per call so a fully-hot M can't deadlock eviction.
func (c *s3FIFO[K]) evictMLocked() bool {
	for i := 0; i < 32; i++ {
		tail := c.mList.Back()
		if tail == nil {
			return false
		}
		entry := tail.Value.(*s3Entry[K])
		if entry.freq > 0 {
			entry.freq--
			c.mList.MoveToFront(tail)
			continue
		}
		c.mList.Remove(tail)
		c.mBytes -= int64(entry.cost)
		delete(c.items, entry.key)
		c.addGhostLocked(entry.key)
		return true
	}
	// All 32 still had freq>0; force-evict the current tail to make
	// progress instead of looping forever.
	tail := c.mList.Back()
	if tail == nil {
		return false
	}
	entry := tail.Value.(*s3Entry[K])
	c.mList.Remove(tail)
	c.mBytes -= int64(entry.cost)
	delete(c.items, entry.key)
	c.addGhostLocked(entry.key)
	return true
}

func (c *s3FIFO[K]) addGhostLocked(key K) {
	if _, ok := c.ghosts[key]; ok {
		return
	}
	elem := c.gList.PushFront(key)
	c.ghosts[key] = elem
	for c.gList.Len() > c.maxGhost {
		old := c.gList.Back()
		c.gList.Remove(old)
		delete(c.ghosts, old.Value.(K))
	}
}

// Delete removes a key from the cache. Returns true if it existed.
func (c *s3FIFO[K]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleteLocked(key)
}

// DeleteBatch removes a batch of keys under a single lock acquisition.
func (c *s3FIFO[K]) DeleteBatch(keys []K) {
	if len(keys) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		c.deleteLocked(k)
	}
}

func (c *s3FIFO[K]) deleteLocked(key K) bool {
	elem, ok := c.items[key]
	if !ok {
		return false
	}
	entry := elem.Value.(*s3Entry[K])
	if entry.inMain {
		c.mList.Remove(elem)
		c.mBytes -= int64(entry.cost)
	} else {
		c.sList.Remove(elem)
		c.sBytes -= int64(entry.cost)
	}
	delete(c.items, key)
	return true
}

// Reset drops all entries (including the ghost set).
func (c *s3FIFO[K]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[K]*list.Element)
	c.ghosts = make(map[K]*list.Element)
	c.sList = list.New()
	c.mList = list.New()
	c.gList = list.New()
	c.sBytes = 0
	c.mBytes = 0
}

// Stats returns (hits, misses, currentBytes, entries) — same shape
// as byteLRU.Stats so callers don't need to distinguish backends.
func (c *s3FIFO[K]) Stats() (hits, misses uint64, bytes int64, entries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), c.sBytes + c.mBytes, len(c.items)
}

// ResetStats clears hit/miss counters without touching cache contents.
func (c *s3FIFO[K]) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
}
