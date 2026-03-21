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

package inference

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// CachedResult stores a verified inference result for precompile access.
type CachedResult struct {
	RequestID types.Hash
	Status    RequestStatus
	OutputCAS types.Hash
	Timestamp time.Time
}

// ResultCache stores verified inference results with LRU eviction and TTL expiry.
// Thread-safe for concurrent access from the EVM precompile and the inference service.
type ResultCache struct {
	mu      sync.RWMutex
	results map[types.Hash]*CachedResult
	order   []types.Hash // LRU order: oldest first
	maxSize int
	ttl     time.Duration
}

// NewResultCache creates a new result cache with the given maximum size and TTL.
// maxSize controls the maximum number of entries before LRU eviction.
// ttl controls how long entries remain valid before expiring.
func NewResultCache(maxSize int, ttl time.Duration) *ResultCache {
	if maxSize <= 0 {
		maxSize = 1024
	}
	return &ResultCache{
		results: make(map[types.Hash]*CachedResult, maxSize),
		order:   make([]types.Hash, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Put adds or updates a cache entry. If the cache is at capacity, the oldest
// entry is evicted before insertion.
func (c *ResultCache) Put(requestID types.Hash, status RequestStatus, outputCAS types.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If entry already exists, update it and move to end of LRU order.
	if existing, ok := c.results[requestID]; ok {
		existing.Status = status
		existing.OutputCAS = outputCAS
		existing.Timestamp = time.Now()
		c.moveToEnd(requestID)
		return
	}

	// Evict oldest if at capacity.
	for len(c.order) >= c.maxSize {
		c.evictOldest()
	}

	c.results[requestID] = &CachedResult{
		RequestID: requestID,
		Status:    status,
		OutputCAS: outputCAS,
		Timestamp: time.Now(),
	}
	c.order = append(c.order, requestID)
}

// Get returns a cached result and true if found and not expired.
// Returns nil and false if the entry does not exist or has expired.
// Expired entries are removed on access.
func (c *ResultCache) Get(requestID types.Hash) (*CachedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.results[requestID]
	if !ok {
		return nil, false
	}

	// Check TTL expiry.
	if c.ttl > 0 && time.Since(entry.Timestamp) > c.ttl {
		c.removeEntry(requestID)
		return nil, false
	}

	// Return a copy to prevent external mutation.
	result := &CachedResult{
		RequestID: entry.RequestID,
		Status:    entry.Status,
		OutputCAS: entry.OutputCAS,
		Timestamp: entry.Timestamp,
	}
	return result, true
}

// Delete removes a specific entry from the cache.
func (c *ResultCache) Delete(requestID types.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeEntry(requestID)
}

// Size returns the current number of entries in the cache.
func (c *ResultCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.results)
}

// Prune removes all expired entries from the cache. Returns the number of
// entries removed.
func (c *ResultCache) Prune() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ttl <= 0 {
		return 0 // no TTL configured, nothing to prune
	}

	now := time.Now()
	pruned := 0

	// Iterate from oldest to newest. Stop when we hit a non-expired entry
	// since all subsequent entries are newer.
	for len(c.order) > 0 {
		oldest := c.order[0]
		entry, ok := c.results[oldest]
		if !ok {
			// Stale order entry; remove it.
			c.order = c.order[1:]
			continue
		}
		if now.Sub(entry.Timestamp) <= c.ttl {
			break // remaining entries are newer
		}
		delete(c.results, oldest)
		c.order = c.order[1:]
		pruned++
	}

	return pruned
}

// evictOldest removes the oldest entry from the cache.
// Must be called with c.mu held.
func (c *ResultCache) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.results, oldest)
}

// removeEntry removes a specific entry from the cache.
// Must be called with c.mu held.
func (c *ResultCache) removeEntry(requestID types.Hash) {
	if _, ok := c.results[requestID]; !ok {
		return
	}
	delete(c.results, requestID)

	// Remove from order slice.
	for i, id := range c.order {
		if id == requestID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// moveToEnd moves an entry to the end of the LRU order (most recently used).
// Must be called with c.mu held.
func (c *ResultCache) moveToEnd(requestID types.Hash) {
	for i, id := range c.order {
		if id == requestID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, requestID)
			break
		}
	}
}
