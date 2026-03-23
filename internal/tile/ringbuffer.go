// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package tile implements a Firedancer-inspired tile-based architecture
// for the N42 blockchain node. Each tile is a long-lived goroutine with
// optional CPU core pinning, connected to other tiles via lock-free
// SPSC (Single Producer Single Consumer) ring buffers.
package tile

import (
	"math/bits"
	"sync/atomic"
)

// cacheLineSize is the typical CPU cache line size in bytes.
// Head and tail pointers are placed on separate cache lines to prevent
// false sharing between the producer and consumer.
const cacheLineSize = 64

// padded64 wraps an atomic.Uint64 with padding to fill a full cache line,
// preventing false sharing when adjacent fields are accessed by different cores.
type padded64 struct {
	v atomic.Uint64
	_ [cacheLineSize - 8]byte
}

// RingBuffer is a lock-free, bounded, single-producer single-consumer queue.
//
// The Lamport queue algorithm is used: the producer owns head and the consumer
// owns tail. Both use atomic load/store (no CAS needed for SPSC). The capacity
// is always a power of 2 for bitwise masking.
//
// Memory layout ensures head and tail are on separate cache lines.
type RingBuffer struct {
	head padded64 // next write index (only producer writes)
	tail padded64 // next read index (only consumer writes)
	mask uint64
	cap  uint64
	// slots uses interface{} (any) stored via atomic.Value for safe cross-goroutine access.
	slots []atomic.Value
}

// NewRingBuffer creates a ring buffer with the given minimum capacity.
// The actual capacity is rounded up to the next power of 2.
func NewRingBuffer(minCapacity int) *RingBuffer {
	if minCapacity < 2 {
		minCapacity = 2
	}
	cap := nextPowerOfTwo(uint64(minCapacity))
	return &RingBuffer{
		mask:  cap - 1,
		cap:   cap,
		slots: make([]atomic.Value, cap),
	}
}

// TryPush attempts to enqueue an item without blocking.
// Returns false if the buffer is full.
// MUST be called from the producer goroutine only.
func (rb *RingBuffer) TryPush(item any) bool {
	head := rb.head.v.Load()
	tail := rb.tail.v.Load()
	if head-tail >= rb.cap {
		return false // full
	}
	rb.slots[head&rb.mask].Store(item)
	rb.head.v.Store(head + 1) // publish to consumer
	return true
}

// TryPop attempts to dequeue an item without blocking.
// Returns nil and false if the buffer is empty.
// MUST be called from the consumer goroutine only.
func (rb *RingBuffer) TryPop() (any, bool) {
	tail := rb.tail.v.Load()
	head := rb.head.v.Load()
	if tail >= head {
		return nil, false // empty
	}
	idx := tail & rb.mask
	item := rb.slots[idx].Load()
	// Note: atomic.Value doesn't support Store(nil), so we rely on
	// the slot being overwritten by TryPush on the next cycle.
	rb.tail.v.Store(tail + 1)
	return item, true
}

// Len returns the approximate number of items in the buffer.
func (rb *RingBuffer) Len() int {
	head := rb.head.v.Load()
	tail := rb.tail.v.Load()
	if head < tail {
		return 0
	}
	return int(head - tail)
}

// Cap returns the buffer capacity (always a power of 2).
func (rb *RingBuffer) Cap() int {
	return int(rb.cap)
}

// IsFull returns true if the buffer has no space for new items.
func (rb *RingBuffer) IsFull() bool {
	return rb.head.v.Load()-rb.tail.v.Load() >= rb.cap
}

// IsEmpty returns true if the buffer has no items to consume.
func (rb *RingBuffer) IsEmpty() bool {
	return rb.tail.v.Load() >= rb.head.v.Load()
}

// Drain resets head and tail to 0, discarding all items.
// Only safe when no producers or consumers are active.
func (rb *RingBuffer) Drain() {
	rb.head.v.Store(0)
	rb.tail.v.Store(0)
}

// nextPowerOfTwo returns the smallest power of 2 >= v.
func nextPowerOfTwo(v uint64) uint64 {
	if v <= 1 {
		return 1
	}
	return 1 << bits.Len64(v-1)
}
