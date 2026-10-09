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
	Removed      uint64 // removed because a block holds them
}

// Entry is one stored frame with the metadata computed at admission.
type Entry struct {
	Raw []byte
	Gas uint64 // sum of the transactions' gas limits (frame.GasEstimate)
	Txs int    // transaction count from the header
}

// tombstoneCap bounds the set of removed frame hashes remembered so a
// resent frame that is already in a block is not admitted again.
const tombstoneCap = 1 << 18

// Store is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	budget int64
	used   int64
	m      map[types.Hash]Entry
	ring   []types.Hash // arrival order; ring[head:] are live or removed
	head   int

	// Frames removed because a block holds them (Remove): a Put of one of
	// these is answered as a duplicate. FIFO-bounded by tombstoneCap.
	tomb     map[types.Hash]struct{}
	tombRing []types.Hash
	tombHead int

	puts, dups, rejected, evicted, evictedBytes, hits, misses, removed atomic.Uint64
}

// New returns a store with the given byte budget (<= 0 selects the default).
func New(budget int64) *Store {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return &Store{budget: budget, m: make(map[types.Hash]Entry), tomb: make(map[types.Hash]struct{})}
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
	return s.PutEntry(h, Entry{Raw: frame})
}

// PutEntry is Put with the admission metadata the block builder reads.
func (s *Store) PutEntry(h types.Hash, e Entry) bool {
	frame := e.Raw
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
	if _, ok := s.tomb[h]; ok {
		s.dups.Add(1)
		return false
	}
	for s.used+size > s.budget && s.head < len(s.ring) {
		s.evictOldestLocked()
	}
	s.m[h] = e
	s.ring = append(s.ring, h)
	s.used += size
	s.puts.Add(1)
	return true
}

func (s *Store) evictOldestLocked() {
	h := s.ring[s.head]
	s.ring[s.head] = types.Hash{}
	s.head++
	if e, ok := s.m[h]; ok {
		s.used -= int64(len(e.Raw))
		delete(s.m, h)
		s.evicted.Add(1)
		s.evictedBytes.Add(uint64(len(e.Raw)))
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
	e, ok := s.m[h]
	s.mu.RUnlock()
	if ok {
		s.hits.Add(1)
	} else {
		s.misses.Add(1)
	}
	return e.Raw, ok
}

// GetEntry returns the frame and its admission metadata.
func (s *Store) GetEntry(h types.Hash) (Entry, bool) {
	s.mu.RLock()
	e, ok := s.m[h]
	s.mu.RUnlock()
	return e, ok
}

// Scan calls fn for each stored frame in arrival order, oldest first,
// until fn returns false. It holds the read lock for the walk, so fn must
// be cheap and must not call back into the store.
func (s *Store) Scan(fn func(h types.Hash, e Entry) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.ring[s.head:] {
		e, ok := s.m[h]
		if !ok {
			continue // removed; its ring slot is reclaimed by eviction
		}
		if !fn(h, e) {
			return
		}
	}
}

// Remove drops frames a block has made durable elsewhere (the Frames
// table) and remembers their hashes so a resend is not admitted again.
// Their ring slots are skipped by Scan and reclaimed as eviction passes.
func (s *Store) Remove(hs []types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hs {
		if e, ok := s.m[h]; ok {
			s.used -= int64(len(e.Raw))
			delete(s.m, h)
			s.removed.Add(1)
		}
		if _, ok := s.tomb[h]; ok {
			continue
		}
		s.tomb[h] = struct{}{}
		s.tombRing = append(s.tombRing, h)
		if len(s.tombRing)-s.tombHead > tombstoneCap {
			delete(s.tomb, s.tombRing[s.tombHead])
			s.tombRing[s.tombHead] = types.Hash{}
			s.tombHead++
			if s.tombHead > 1024 && s.tombHead*2 > len(s.tombRing) {
				n := copy(s.tombRing, s.tombRing[s.tombHead:])
				s.tombRing = s.tombRing[:n]
				s.tombHead = 0
			}
		}
	}
	// A dead ring prefix is reclaimed here too, so a store that is drained
	// by blocks rather than by eviction does not grow its ring forever.
	for s.head < len(s.ring) {
		if _, ok := s.m[s.ring[s.head]]; ok {
			break
		}
		s.ring[s.head] = types.Hash{}
		s.head++
	}
	if s.head > 1024 && s.head*2 > len(s.ring) {
		n := copy(s.ring, s.ring[s.head:])
		s.ring = s.ring[:n]
		s.head = 0
	}
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
	st.Removed = s.removed.Load()
	return st
}
