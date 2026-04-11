// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// BlockDedup is a small bounded set of recently-seen (blockNumber, blockHash)
// pairs. The mobile facade uses it to ensure that, when a phone is
// connected to N producers simultaneously, the SAME block pushed by
// multiple producers is verified and BLS-signed exactly once.
//
// Both V1 (state.EntireCode over JSON-RPC subscribe) and V2 (StreamPacket)
// receive paths consult this dedup before doing the BLS-sign work.
//
// Why a custom small LRU instead of hashicorp/golang-lru: keeps
// cmd/evmsdk's gomobile bind footprint as small as possible. The
// implementation is ~80 LOC, allocation-free in steady state, and
// guaranteed bounded.

package evmsdk

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// defaultDedupCapacity is the number of distinct (number, hash) pairs
// to remember. Sized for several minutes of mainnet activity at 12s
// block time (~25 entries / 5 min) with comfortable headroom — even a
// device subscribed to 10 producers seeing every block within a 1024-
// entry window covers ~3.4 hours of unique blocks.
const defaultDedupCapacity = 1024

// blockDedupKey is the lookup key. Keeping number AND hash both binds
// the entry to a specific block (so a fork at the same height with a
// different hash is correctly counted as a separate, un-seen block).
type blockDedupKey struct {
	number uint64
	hash   types.Hash
}

// BlockDedup is a fixed-capacity LRU keyed on (blockNumber, blockHash).
// All methods are goroutine-safe.
type BlockDedup struct {
	mu       sync.Mutex
	capacity int
	seen     map[blockDedupKey]int // value is the index in order
	order    []blockDedupKey       // ring buffer
	next     int                   // next write position in order
}

// NewBlockDedup constructs a new dedup with the given capacity. A
// capacity of zero or below is clamped to defaultDedupCapacity.
func NewBlockDedup(capacity int) *BlockDedup {
	if capacity <= 0 {
		capacity = defaultDedupCapacity
	}
	return &BlockDedup{
		capacity: capacity,
		seen:     make(map[blockDedupKey]int, capacity),
		order:    make([]blockDedupKey, capacity),
	}
}

// Mark records (number, hash) and returns true iff this is the FIRST
// time the pair has been seen since (capacity) other unique pairs ago.
//
// Subsequent calls with the same key within the LRU window return false
// — callers should treat false as "skip BLS sign, already done".
func (d *BlockDedup) Mark(number uint64, hash types.Hash) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := blockDedupKey{number: number, hash: hash}
	if _, ok := d.seen[key]; ok {
		return false
	}

	// Evict the slot we're about to overwrite, if it holds something.
	old := d.order[d.next]
	if old != (blockDedupKey{}) {
		// Only evict if the map still maps it to this slot — guards
		// against the rare case of an explicit Mark of the zero key.
		if idx, ok := d.seen[old]; ok && idx == d.next {
			delete(d.seen, old)
		}
	}

	d.seen[key] = d.next
	d.order[d.next] = key
	d.next = (d.next + 1) % d.capacity
	return true
}

// Has reports whether the (number, hash) pair is currently in the LRU
// window without recording it. Useful for tests.
func (d *BlockDedup) Has(number uint64, hash types.Hash) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.seen[blockDedupKey{number: number, hash: hash}]
	return ok
}

// Len returns the number of entries currently held (≤ capacity).
func (d *BlockDedup) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// Reset clears all entries.
func (d *BlockDedup) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.seen {
		delete(d.seen, k)
	}
	for i := range d.order {
		d.order[i] = blockDedupKey{}
	}
	d.next = 0
}
