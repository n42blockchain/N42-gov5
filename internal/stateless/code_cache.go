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

package stateless

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// CodeCache stores contract bytecodes in memory, keyed by code hash.
// Used by stateless nodes to avoid re-fetching bytecodes with each witness.
//
// When a witness arrives, its contract codes are loaded into the cache.
// Subsequent blocks can reuse cached codes instead of requiring them in
// every witness, reducing witness size for long-lived contracts.
//
// Thread-safe for concurrent reads and writes.
type CodeCache struct {
	mu    sync.RWMutex
	codes map[types.Hash][]byte
	cap   int
}

// NewCodeCache creates a CodeCache with the given maximum capacity.
// If capacity is <= 0, a default of 4096 is used.
func NewCodeCache(capacity int) *CodeCache {
	if capacity <= 0 {
		capacity = 4096
	}
	return &CodeCache{
		codes: make(map[types.Hash][]byte, capacity),
		cap:   capacity,
	}
}

// Get returns the contract bytecode for the given code hash.
// Returns (code, true) if found, (nil, false) if not cached.
// The returned slice is a copy -- callers may modify it safely.
func (c *CodeCache) Get(codeHash types.Hash) ([]byte, bool) {
	c.mu.RLock()
	code, ok := c.codes[codeHash]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(code))
	copy(cp, code)
	return cp, true
}

// Put stores a contract bytecode under its code hash.
// If the cache is at capacity, the insertion is silently dropped.
// A nil or empty code slice is a valid entry (e.g., for self-destructed contracts).
func (c *CodeCache) Put(codeHash types.Hash, code []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already present, update in place (no capacity change).
	if _, exists := c.codes[codeHash]; exists {
		cp := make([]byte, len(code))
		copy(cp, code)
		c.codes[codeHash] = cp
		return
	}

	// Enforce capacity.
	if len(c.codes) >= c.cap {
		return
	}

	cp := make([]byte, len(code))
	copy(cp, code)
	c.codes[codeHash] = cp
}

// Len returns the number of entries in the cache.
func (c *CodeCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.codes)
}
