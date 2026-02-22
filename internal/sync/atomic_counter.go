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
	value atomic.Int64
}

// NewAtomicInt64 creates a new atomic int64 with initial value.
func NewAtomicInt64(initial int64) *AtomicInt64 {
	a := &AtomicInt64{}
	a.value.Store(initial)
	return a
}

func (a *AtomicInt64) Load() int64 {
	return a.value.Load()
}

func (a *AtomicInt64) Store(val int64) {
	a.value.Store(val)
}

func (a *AtomicInt64) Add(delta int64) int64 {
	return a.value.Add(delta)
}

// Inc increments by 1 and returns the new value.
func (a *AtomicInt64) Inc() int64 {
	return a.value.Add(1)
}

// Dec decrements by 1 and returns the new value.
func (a *AtomicInt64) Dec() int64 {
	return a.value.Add(-1)
}

func (a *AtomicInt64) CompareAndSwap(old, new int64) bool {
	return a.value.CompareAndSwap(old, new)
}

// AtomicUint64 is a lock-free uint64 counter.
type AtomicUint64 struct {
	value atomic.Uint64
}

// NewAtomicUint64 creates a new atomic uint64 with initial value.
func NewAtomicUint64(initial uint64) *AtomicUint64 {
	a := &AtomicUint64{}
	a.value.Store(initial)
	return a
}

func (a *AtomicUint64) Load() uint64 {
	return a.value.Load()
}

func (a *AtomicUint64) Store(val uint64) {
	a.value.Store(val)
}

func (a *AtomicUint64) Add(delta uint64) uint64 {
	return a.value.Add(delta)
}

// Inc increments by 1 and returns the new value.
func (a *AtomicUint64) Inc() uint64 {
	return a.value.Add(1)
}

func (a *AtomicUint64) CompareAndSwap(old, new uint64) bool {
	return a.value.CompareAndSwap(old, new)
}

// AtomicBool is a lock-free boolean.
type AtomicBool struct {
	value atomic.Bool
}

// NewAtomicBool creates a new atomic bool.
func NewAtomicBool(initial bool) *AtomicBool {
	a := &AtomicBool{}
	a.value.Store(initial)
	return a
}

func (a *AtomicBool) Load() bool {
	return a.value.Load()
}

func (a *AtomicBool) Store(val bool) {
	a.value.Store(val)
}

func (a *AtomicBool) CompareAndSwap(old, new bool) bool {
	return a.value.CompareAndSwap(old, new)
}

// Toggle atomically flips the boolean and returns the new value.
func (a *AtomicBool) Toggle() bool {
	for {
		current := a.value.Load()
		toggled := !current
		if a.value.CompareAndSwap(current, toggled) {
			return toggled
		}
	}
}
