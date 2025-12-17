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

package cache

import (
	"container/list"
	"sync"
)

// LRU is a thread-safe LRU cache implementation with generics.
type LRU[K comparable, V any] struct {
	capacity int
	items    map[K]*list.Element
	order    *list.List
	mu       sync.RWMutex
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

// NewLRU creates a new LRU cache with the given capacity.
func NewLRU[K comparable, V any](capacity int) *LRU[K, V] {
	return &LRU[K, V]{
		capacity: capacity,
		items:    make(map[K]*list.Element),
		order:    list.New(),
	}
}

// Get retrieves a value from the cache.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*lruEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Peek retrieves a value without updating recency.
func (c *LRU[K, V]) Peek(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if elem, ok := c.items[key]; ok {
		return elem.Value.(*lruEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Set adds or updates a value in the cache.
func (c *LRU[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*lruEntry[K, V]).value = value
		return
	}

	// Evict if at capacity
	if c.order.Len() >= c.capacity {
		c.evictOldest()
	}

	entry := &lruEntry[K, V]{key: key, value: value}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
}

// Delete removes a key from the cache.
func (c *LRU[K, V]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.removeElement(elem)
		return true
	}
	return false
}

// Contains checks if a key exists in the cache without updating recency.
func (c *LRU[K, V]) Contains(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.items[key]
	return ok
}

// Len returns the current number of items in the cache.
func (c *LRU[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len()
}

// Clear removes all items from the cache.
func (c *LRU[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[K]*list.Element)
	c.order.Init()
}

// Keys returns all keys in the cache, from most to least recent.
func (c *LRU[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]K, 0, c.order.Len())
	for elem := c.order.Front(); elem != nil; elem = elem.Next() {
		keys = append(keys, elem.Value.(*lruEntry[K, V]).key)
	}
	return keys
}

func (c *LRU[K, V]) evictOldest() {
	if oldest := c.order.Back(); oldest != nil {
		c.removeElement(oldest)
	}
}

func (c *LRU[K, V]) removeElement(elem *list.Element) {
	c.order.Remove(elem)
	entry := elem.Value.(*lruEntry[K, V])
	delete(c.items, entry.key)
}

// ARC is a simplified Adaptive Replacement Cache.
// It maintains two LRU lists: T1 for recently used items and T2 for frequently used items.
type ARC[K comparable, V any] struct {
	capacity int
	p        int // Target size for T1

	t1    *LRU[K, V] // Recently used
	t2    *LRU[K, V] // Frequently used
	b1    *LRU[K, struct{}] // Ghost entries for T1
	b2    *LRU[K, struct{}] // Ghost entries for T2

	mu sync.Mutex
}

// NewARC creates a new ARC cache with the given capacity.
func NewARC[K comparable, V any](capacity int) *ARC[K, V] {
	return &ARC[K, V]{
		capacity: capacity,
		t1:       NewLRU[K, V](capacity),
		t2:       NewLRU[K, V](capacity),
		b1:       NewLRU[K, struct{}](capacity),
		b2:       NewLRU[K, struct{}](capacity),
	}
}

// Get retrieves a value from the cache.
func (c *ARC[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check T2 first (frequently used)
	if val, ok := c.t2.Get(key); ok {
		return val, true
	}

	// Check T1 (recently used)
	if val, ok := c.t1.Peek(key); ok {
		// Move to T2
		c.t1.Delete(key)
		c.t2.Set(key, val)
		return val, true
	}

	var zero V
	return zero, false
}

// Set adds or updates a value in the cache.
func (c *ARC[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If in T2, update
	if c.t2.Contains(key) {
		c.t2.Set(key, value)
		return
	}

	// If in T1, promote to T2
	if c.t1.Contains(key) {
		c.t1.Delete(key)
		c.t2.Set(key, value)
		return
	}

	// If in B1 ghost list, adjust p and add to T2
	if c.b1.Contains(key) {
		delta := 1
		if c.b2.Len() > c.b1.Len() {
			delta = c.b2.Len() / c.b1.Len()
		}
		c.p = min(c.p+delta, c.capacity)
		c.b1.Delete(key)
		c.replace(key)
		c.t2.Set(key, value)
		return
	}

	// If in B2 ghost list, adjust p and add to T2
	if c.b2.Contains(key) {
		delta := 1
		if c.b1.Len() > c.b2.Len() {
			delta = c.b1.Len() / c.b2.Len()
		}
		c.p = max(c.p-delta, 0)
		c.b2.Delete(key)
		c.replace(key)
		c.t2.Set(key, value)
		return
	}

	// Not in cache, add to T1
	if c.t1.Len()+c.b1.Len() >= c.capacity {
		if c.t1.Len() < c.capacity {
			c.b1.evictOldest()
			c.replace(key)
		} else {
			c.t1.evictOldest()
		}
	} else if c.t1.Len()+c.t2.Len()+c.b1.Len()+c.b2.Len() >= c.capacity {
		if c.t1.Len()+c.t2.Len()+c.b1.Len()+c.b2.Len() >= 2*c.capacity {
			c.b2.evictOldest()
		}
		c.replace(key)
	}
	c.t1.Set(key, value)
}

func (c *ARC[K, V]) replace(key K) {
	if c.t1.Len() > 0 && (c.t1.Len() > c.p || (c.b2.Contains(key) && c.t1.Len() == c.p)) {
		// Move from T1 to B1
		if keys := c.t1.Keys(); len(keys) > 0 {
			oldKey := keys[len(keys)-1]
			c.t1.Delete(oldKey)
			c.b1.Set(oldKey, struct{}{})
		}
	} else if c.t2.Len() > 0 {
		// Move from T2 to B2
		if keys := c.t2.Keys(); len(keys) > 0 {
			oldKey := keys[len(keys)-1]
			c.t2.Delete(oldKey)
			c.b2.Set(oldKey, struct{}{})
		}
	}
}

// Len returns the total number of items in the cache.
func (c *ARC[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t1.Len() + c.t2.Len()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

