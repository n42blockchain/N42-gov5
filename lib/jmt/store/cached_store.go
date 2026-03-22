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

package store

import (
	"container/list"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/lib/jmt"
)

// CachedStore wraps a NodeStore with a read-through LRU byte cache.
// This avoids opening a new MDBX transaction per Get() call when the
// backing store is a LazyDBStore.
type CachedStore struct {
	inner    jmt.NodeStore
	mu       sync.RWMutex
	cache    map[jmt.Hash]*list.Element
	lru      *list.List
	capacity int
	hits     atomic.Uint64
	misses   atomic.Uint64
}

type cacheEntry struct {
	hash jmt.Hash
	data []byte
}

// NewCachedStore creates a CachedStore wrapping inner with the given capacity.
func NewCachedStore(inner jmt.NodeStore, capacity int) *CachedStore {
	if capacity <= 0 {
		capacity = 4096
	}
	return &CachedStore{
		inner:    inner,
		cache:    make(map[jmt.Hash]*list.Element, capacity),
		lru:      list.New(),
		capacity: capacity,
	}
}

// Get retrieves a serialized node by hash. It checks the cache first,
// then falls through to the inner store on a miss.
func (s *CachedStore) Get(hash jmt.Hash) ([]byte, error) {
	// 1. Check cache under write lock (promotion requires write access).
	s.mu.Lock()
	if elem, ok := s.cache[hash]; ok {
		s.lru.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		cp := make([]byte, len(entry.data))
		copy(cp, entry.data)
		s.mu.Unlock()
		s.hits.Add(1)
		return cp, nil
	}
	s.mu.Unlock()

	// 2. Cache miss — read from inner store.
	data, err := s.inner.Get(hash)
	if err != nil {
		s.misses.Add(1)
		return nil, err
	}

	// 3. Add to cache under write lock. Double-check in case another
	// goroutine populated the same key while we read from inner.
	s.mu.Lock()
	s.addLocked(hash, data)
	s.mu.Unlock()

	s.misses.Add(1)

	// Return a copy.
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// Put writes a node to the inner store and also caches it.
func (s *CachedStore) Put(hash jmt.Hash, data []byte) error {
	if err := s.inner.Put(hash, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.addLocked(hash, data)
	s.mu.Unlock()
	return nil
}

// Delete removes a node from the inner store and the cache.
func (s *CachedStore) Delete(hash jmt.Hash) error {
	if err := s.inner.Delete(hash); err != nil {
		return err
	}
	s.mu.Lock()
	if elem, ok := s.cache[hash]; ok {
		s.lru.Remove(elem)
		delete(s.cache, hash)
	}
	s.mu.Unlock()
	return nil
}

// Has checks whether a node exists, first in the cache then in the inner store.
func (s *CachedStore) Has(hash jmt.Hash) (bool, error) {
	s.mu.RLock()
	_, ok := s.cache[hash]
	s.mu.RUnlock()
	if ok {
		return true, nil
	}
	return s.inner.Has(hash)
}

// Populate adds data to the cache without writing to the inner store.
// This is useful for promoting flushed nodes into the cache (e.g., after FlushTo).
func (s *CachedStore) Populate(hash jmt.Hash, data []byte) {
	s.mu.Lock()
	s.addLocked(hash, data)
	s.mu.Unlock()
}

// Stats returns the cumulative hit and miss counts.
func (s *CachedStore) Stats() (hits, misses uint64) {
	return s.hits.Load(), s.misses.Load()
}

// addLocked inserts an entry into the cache, evicting the LRU entry if at capacity.
// Caller must hold s.mu write lock.
func (s *CachedStore) addLocked(hash jmt.Hash, data []byte) {
	// Already cached — update and promote.
	if elem, ok := s.cache[hash]; ok {
		entry := elem.Value.(*cacheEntry)
		cp := make([]byte, len(data))
		copy(cp, data)
		entry.data = cp
		s.lru.MoveToFront(elem)
		return
	}

	// Evict LRU entry if at capacity.
	if s.lru.Len() >= s.capacity {
		tail := s.lru.Back()
		if tail != nil {
			evicted := s.lru.Remove(tail).(*cacheEntry)
			delete(s.cache, evicted.hash)
		}
	}

	cp := make([]byte, len(data))
	copy(cp, data)
	entry := &cacheEntry{hash: hash, data: cp}
	elem := s.lru.PushFront(entry)
	s.cache[hash] = elem
}

// Compile-time check that CachedStore implements jmt.NodeStore.
var _ jmt.NodeStore = (*CachedStore)(nil)
