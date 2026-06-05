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

package vm

import "testing"

// TestGetByteSliceZeroesRecycled pins the consensus-safety invariant: a buffer
// returned from the pool must be zeroed, so it is a transparent drop-in for
// make([]byte, n) and never leaks a previous user's bytes.
func TestGetByteSliceZeroesRecycled(t *testing.T) {
	b := GetByteSlice(32)
	for i := range b {
		b[i] = 0xFF // dirty it
	}
	PutByteSlice(b)

	// The pool may or may not hand back the same backing array; loop a few
	// times to make a recycle overwhelmingly likely, and assert every byte of
	// every returned buffer is zero.
	for n := 0; n < 64; n++ {
		got := GetByteSlice(32)
		for i, v := range got {
			if v != 0 {
				t.Fatalf("GetByteSlice returned non-zero byte %d=%#x (stale recycle)", i, v)
			}
		}
		PutByteSlice(got)
	}
}

// TestGetMemoryZeroesRecycled: EVM memory must be zero-initialised; a recycled
// pool buffer must come back zeroed.
func TestGetMemoryZeroesRecycled(t *testing.T) {
	const size = 256
	m := GetMemory(size)
	for i := range m {
		m[i] = 0xAB
	}
	PutMemory(m)

	for n := 0; n < 64; n++ {
		got := GetMemory(size)
		for i, v := range got {
			if v != 0 {
				t.Fatalf("GetMemory returned non-zero byte %d=%#x (stale recycle)", i, v)
			}
		}
		PutMemory(got)
	}
}
