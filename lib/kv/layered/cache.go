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
	"container/list"
	"sync"
	"sync/atomic"
)

// cacheKey combines table name and key into a single lookup key.
type cacheKey struct {
	table string
	key   string // string([]byte) for map compatibility
}

type cacheEntry struct {
	key   cacheKey
	value []byte // nil means "key not found" (negative cache)
	elem  *list.Element
}

// cacheShard is a single LRU shard with its own lock.
type cacheShard struct {
	mu       sync.RWMutex
	items    map[cacheKey]*cacheEntry
	evictLRU *list.List
	capacity int
}

func newCacheShard(capacity int) *cacheShard {
	return &cacheShard{
		items:    make(map[cacheKey]*cacheEntry, capacity/4),
		evictLRU: list.New(),
		capacity: capacity,
	}
}

func (s *cacheShard) get(k cacheKey) ([]byte, bool) {
	s.mu.RLock()
	entry, ok := s.items[k]
	if ok {
		// Promote to front (needs write lock, but defer to avoid contention on hot path).
		// Use RLock for the common case; promote lazily.
		val := entry.value
		s.mu.RUnlock()

		// Lazy promote: upgrade to write lock.
		s.mu.Lock()
		if entry.elem != nil {
			s.evictLRU.MoveToFront(entry.elem)
		}
		s.mu.Unlock()

		return val, true
	}
	s.mu.RUnlock()
	return nil, false
}

func (s *cacheShard) put(k cacheKey, v []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.items[k]; ok {
		entry.value = v
		s.evictLRU.MoveToFront(entry.elem)
		return
	}

	// Evict if at capacity.
	for s.evictLRU.Len() >= s.capacity {
		tail := s.evictLRU.Back()
		if tail == nil {
			break
		}
		evicted := tail.Value.(*cacheEntry)
		s.evictLRU.Remove(tail)
		delete(s.items, evicted.key)
	}

	entry := &cacheEntry{key: k, value: v}
	entry.elem = s.evictLRU.PushFront(entry)
	s.items[k] = entry
}

func (s *cacheShard) del(k cacheKey) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.items[k]; ok {
		s.evictLRU.Remove(entry.elem)
		delete(s.items, entry.key)
	}
}

func (s *cacheShard) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[cacheKey]*cacheEntry, s.capacity/4)
	s.evictLRU.Init()
}

func (s *cacheShard) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// ShardedCache is a concurrent LRU cache split across N shards to reduce
// lock contention. Each shard has its own LRU eviction list and capacity.
type ShardedCache struct {
	shards    []*cacheShard
	shardMask uint32
	hits      atomic.Uint64
	misses    atomic.Uint64
}

// NewShardedCache creates a new sharded LRU cache.
// shardCount must be a power of 2. capacityPerShard is the max entries per shard.
func NewShardedCache(shardCount, capacityPerShard int) *ShardedCache {
	if shardCount <= 0 {
		shardCount = DefaultCacheShards
	}
	if capacityPerShard <= 0 {
		capacityPerShard = DefaultCacheCapacity
	}

	shards := make([]*cacheShard, shardCount)
	for i := range shards {
		shards[i] = newCacheShard(capacityPerShard)
	}

	return &ShardedCache{
		shards:    shards,
		shardMask: uint32(shardCount - 1),
	}
}

// shard returns the shard for the given table+key using FNV-1a hash.
func (c *ShardedCache) shard(table string, key []byte) *cacheShard {
	h := uint32(2166136261) // FNV offset basis
	for i := 0; i < len(table); i++ {
		h ^= uint32(table[i])
		h *= 16777619 // FNV prime
	}
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return c.shards[h&c.shardMask]
}

// Get retrieves a value from the cache.
// Returns (value, true) on hit, (nil, false) on miss.
// A nil value with ok=true means "negative cache" — the key is known to not exist.
func (c *ShardedCache) Get(table string, key []byte) ([]byte, bool) {
	k := cacheKey{table: table, key: string(key)}
	v, ok := c.shard(table, key).get(k)
	if ok {
		c.hits.Add(1)
	} else {
		c.misses.Add(1)
	}
	return v, ok
}

// Put stores a value in the cache (write-through: caller must also write to DB).
func (c *ShardedCache) Put(table string, key, value []byte) {
	k := cacheKey{table: table, key: string(key)}
	// Copy value to avoid holding references to mmap'd memory.
	var v []byte
	if value != nil {
		v = make([]byte, len(value))
		copy(v, value)
	}
	c.shard(table, key).put(k, v)
}

// Delete removes a key from the cache.
func (c *ShardedCache) Delete(table string, key []byte) {
	k := cacheKey{table: table, key: string(key)}
	c.shard(table, key).del(k)
}

// Clear empties the entire cache.
func (c *ShardedCache) Clear() {
	for _, s := range c.shards {
		s.clear()
	}
	c.hits.Store(0)
	c.misses.Store(0)
}

// Stats returns cache hit/miss statistics.
func (c *ShardedCache) Stats() (hits, misses uint64, entries int) {
	hits = c.hits.Load()
	misses = c.misses.Load()
	for _, s := range c.shards {
		entries += s.len()
	}
	return
}
