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

package sync

import (
	"sync/atomic"
)

// AtomicInt64 is a lock-free int64 counter.
type AtomicInt64 struct {
	value int64
}

// NewAtomicInt64 creates a new atomic int64 with initial value.
func NewAtomicInt64(initial int64) *AtomicInt64 {
	return &AtomicInt64{value: initial}
}

// Load returns the current value.
func (a *AtomicInt64) Load() int64 {
	return atomic.LoadInt64(&a.value)
}

// Store sets the value.
func (a *AtomicInt64) Store(val int64) {
	atomic.StoreInt64(&a.value, val)
}

// Add adds delta and returns the new value.
func (a *AtomicInt64) Add(delta int64) int64 {
	return atomic.AddInt64(&a.value, delta)
}

// Inc increments by 1 and returns the new value.
func (a *AtomicInt64) Inc() int64 {
	return atomic.AddInt64(&a.value, 1)
}

// Dec decrements by 1 and returns the new value.
func (a *AtomicInt64) Dec() int64 {
	return atomic.AddInt64(&a.value, -1)
}

// CompareAndSwap performs a CAS operation.
func (a *AtomicInt64) CompareAndSwap(old, new int64) bool {
	return atomic.CompareAndSwapInt64(&a.value, old, new)
}

// AtomicUint64 is a lock-free uint64 counter.
type AtomicUint64 struct {
	value uint64
}

// NewAtomicUint64 creates a new atomic uint64 with initial value.
func NewAtomicUint64(initial uint64) *AtomicUint64 {
	return &AtomicUint64{value: initial}
}

// Load returns the current value.
func (a *AtomicUint64) Load() uint64 {
	return atomic.LoadUint64(&a.value)
}

// Store sets the value.
func (a *AtomicUint64) Store(val uint64) {
	atomic.StoreUint64(&a.value, val)
}

// Add adds delta and returns the new value.
func (a *AtomicUint64) Add(delta uint64) uint64 {
	return atomic.AddUint64(&a.value, delta)
}

// Inc increments by 1 and returns the new value.
func (a *AtomicUint64) Inc() uint64 {
	return atomic.AddUint64(&a.value, 1)
}

// CompareAndSwap performs a CAS operation.
func (a *AtomicUint64) CompareAndSwap(old, new uint64) bool {
	return atomic.CompareAndSwapUint64(&a.value, old, new)
}

// AtomicBool is a lock-free boolean.
type AtomicBool struct {
	value int32
}

// NewAtomicBool creates a new atomic bool.
func NewAtomicBool(initial bool) *AtomicBool {
	a := &AtomicBool{}
	if initial {
		a.value = 1
	}
	return a
}

// Load returns the current value.
func (a *AtomicBool) Load() bool {
	return atomic.LoadInt32(&a.value) == 1
}

// Store sets the value.
func (a *AtomicBool) Store(val bool) {
	if val {
		atomic.StoreInt32(&a.value, 1)
	} else {
		atomic.StoreInt32(&a.value, 0)
	}
}

// CompareAndSwap performs a CAS operation.
func (a *AtomicBool) CompareAndSwap(old, new bool) bool {
	var oldVal, newVal int32
	if old {
		oldVal = 1
	}
	if new {
		newVal = 1
	}
	return atomic.CompareAndSwapInt32(&a.value, oldVal, newVal)
}

// Toggle toggles the boolean and returns the new value.
func (a *AtomicBool) Toggle() bool {
	for {
		old := atomic.LoadInt32(&a.value)
		newVal := int32(1)
		if old == 1 {
			newVal = 0
		}
		if atomic.CompareAndSwapInt32(&a.value, old, newVal) {
			return newVal == 1
		}
	}
}

