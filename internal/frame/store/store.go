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

// Package store holds received frames keyed by FrameHash in a byte-budgeted
// ring: frames are kept in arrival order and the oldest are evicted when the
// budget is exceeded. Each frame is one pointer-free []byte, so the garbage
// collector marks it without scanning its contents; the per-frame overhead
// on the scanned heap is one map entry and one ring slot.
package store

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// DefaultBudget is the default byte budget (4 GiB).
const DefaultBudget int64 = 4 << 30

// EnvBudget names the environment variable that overrides the budget.
const EnvBudget = "N42_FRAME_STORE_BYTES"

// Stats is a snapshot of the store's counters.
type Stats struct {
	Frames       int
	Bytes        int64
	Budget       int64
	Puts         uint64
	Duplicates   uint64
	Rejected     uint64 // larger than the whole budget
	Evicted      uint64
	EvictedBytes uint64
	Hits         uint64
	Misses       uint64
}

// Store is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	budget int64
	used   int64
	m      map[types.Hash][]byte
	ring   []types.Hash // arrival order; ring[head:] are live
	head   int

	puts, dups, rejected, evicted, evictedBytes, hits, misses atomic.Uint64
}

// New returns a store with the given byte budget (<= 0 selects the default).
func New(budget int64) *Store {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return &Store{budget: budget, m: make(map[types.Hash][]byte)}
}

// NewFromEnv returns a store sized by N42_FRAME_STORE_BYTES, or the default.
func NewFromEnv() *Store {
	var b int64
	if v := os.Getenv(EnvBudget); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			b = n
		}
	}
	return New(b)
}

// Put stores frame under h. It returns false when h is already present or
// the frame alone exceeds the budget. The store keeps frame as given; the
// caller must not modify it afterwards.
func (s *Store) Put(h types.Hash, frame []byte) bool {
	size := int64(len(frame))
	if size > s.budget {
		s.rejected.Add(1)
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[h]; ok {
		s.dups.Add(1)
		return false
	}
	for s.used+size > s.budget && s.head < len(s.ring) {
		s.evictOldestLocked()
	}
	s.m[h] = frame
	s.ring = append(s.ring, h)
	s.used += size
	s.puts.Add(1)
	return true
}

func (s *Store) evictOldestLocked() {
	h := s.ring[s.head]
	s.ring[s.head] = types.Hash{}
	s.head++
	if b, ok := s.m[h]; ok {
		s.used -= int64(len(b))
		delete(s.m, h)
		s.evicted.Add(1)
		s.evictedBytes.Add(uint64(len(b)))
	}
	// Compact once the dead prefix dominates, so the ring does not grow
	// without bound under steady eviction.
	if s.head > 1024 && s.head*2 > len(s.ring) {
		n := copy(s.ring, s.ring[s.head:])
		s.ring = s.ring[:n]
		s.head = 0
	}
}

// Get returns the frame stored under h.
func (s *Store) Get(h types.Hash) ([]byte, bool) {
	s.mu.RLock()
	b, ok := s.m[h]
	s.mu.RUnlock()
	if ok {
		s.hits.Add(1)
	} else {
		s.misses.Add(1)
	}
	return b, ok
}

// Has reports whether h is stored.
func (s *Store) Has(h types.Hash) bool {
	s.mu.RLock()
	_, ok := s.m[h]
	s.mu.RUnlock()
	return ok
}

// Len returns the number of stored frames.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// Stats returns a snapshot of the counters.
func (s *Store) Stats() Stats {
	s.mu.RLock()
	st := Stats{Frames: len(s.m), Bytes: s.used, Budget: s.budget}
	s.mu.RUnlock()
	st.Puts = s.puts.Load()
	st.Duplicates = s.dups.Load()
	st.Rejected = s.rejected.Load()
	st.Evicted = s.evicted.Load()
	st.EvictedBytes = s.evictedBytes.Load()
	st.Hits = s.hits.Load()
	st.Misses = s.misses.Load()
	return st
}
