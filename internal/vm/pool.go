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
//
// sync.Pool recyclers for hot-path EVM allocations. Uint256Pool
// (GetUint256/PutUint256) hands out *uint256.Int values cleared on return;
// ByteSlicePool/HashPool/memPool recycle word/hash/memory buffers, zeroed on
// Get so each helper is a transparent drop-in for make([]byte, n).
//
// NOTE (2026-06-05 audit): these byte/memory pools are currently NOT wired into
// the interpreter — the live EVM memory recycling uses the *Memory struct pool
// in interpreter.go (which zeroes via Memory.Resize). These helpers are kept as
// scaffolding; if wired later, the Get-time clear() above is what keeps EVM
// memory zero-initialised. Only Uint256Pool/Put semantics are exercised (tests).

package vm

import (
	"sync"

	"github.com/holiman/uint256"
)

// Uint256Pool is a pool of *uint256.Int to reduce allocations in hot paths.
var Uint256Pool = &sync.Pool{
	New: func() any {
		return new(uint256.Int)
	},
}

// GetUint256 gets a *uint256.Int from the pool.
func GetUint256() *uint256.Int {
	v, ok := Uint256Pool.Get().(*uint256.Int)
	if !ok {
		return new(uint256.Int)
	}
	return v
}

// PutUint256 returns a *uint256.Int to the pool after clearing it.
func PutUint256(v *uint256.Int) {
	if v != nil {
		v.Clear()
		Uint256Pool.Put(v)
	}
}

// ByteSlicePool is a pool for byte slices used in memory operations.
var ByteSlicePool = &sync.Pool{
	New: func() any {
		// Default to 32 bytes (common size for words)
		b := make([]byte, 32)
		return &b
	},
}

// GetByteSlice gets a byte slice from the pool with at least the given capacity.
// The returned slice is zeroed, so it is a transparent drop-in for
// make([]byte, size): a recycled buffer still carries the previous user's bytes
// and returning it unzeroed would leak stale data (a consensus hazard if ever
// wired into EVM memory/word buffers, which must be zero-initialised).
func GetByteSlice(size int) []byte {
	if size <= 32 {
		bp, ok := ByteSlicePool.Get().(*[]byte)
		if !ok || bp == nil {
			// Type assertion failed, allocate new slice
			return make([]byte, size)
		}
		s := (*bp)[:size]
		clear(s)
		return s
	}
	return make([]byte, size)
}

// PutByteSlice returns a byte slice to the pool if it's the right size.
func PutByteSlice(b []byte) {
	if cap(b) == 32 {
		bp := b[:32]
		ByteSlicePool.Put(&bp)
	}
}

// HashPool is a pool for hash results (32 bytes).
var HashPool = &sync.Pool{
	New: func() any {
		b := make([]byte, 32)
		return &b
	},
}

// GetHashBuffer gets a 32-byte buffer from the pool.
func GetHashBuffer() *[]byte {
	bp, ok := HashPool.Get().(*[]byte)
	if !ok || bp == nil {
		// Type assertion failed, allocate new buffer
		b := make([]byte, 32)
		return &b
	}
	return bp
}

// PutHashBuffer returns a 32-byte buffer to the pool.
func PutHashBuffer(b *[]byte) {
	if b != nil && len(*b) == 32 {
		HashPool.Put(b)
	}
}

// MemoryPool provides memory slices for EVM memory operations.
type MemoryPool struct {
	pools []*sync.Pool
}

// Global memory pool with different size classes
var memPool = &MemoryPool{
	pools: make([]*sync.Pool, 20), // 2^0 to 2^19 (1B to 512KB)
}

func init() {
	for i := range memPool.pools {
		size := 1 << uint(i)
		memPool.pools[i] = &sync.Pool{
			New: func() any {
				b := make([]byte, size)
				return &b
			},
		}
	}
}

// sizeClass returns the pool index for a given size.
func sizeClass(size int) int {
	if size <= 0 {
		return 0
	}
	// Find the smallest power of 2 >= size
	class := 0
	s := size - 1
	for s > 0 {
		s >>= 1
		class++
	}
	if class >= len(memPool.pools) {
		return -1 // Too large for pool
	}
	return class
}

// GetMemory gets a memory slice of at least the given size. The returned slice
// is zeroed so it is a transparent drop-in for make([]byte, size): EVM memory
// must be zero-initialised, and a recycled buffer otherwise carries the previous
// user's bytes (a consensus hazard).
func GetMemory(size int) []byte {
	class := sizeClass(size)
	if class < 0 {
		return make([]byte, size)
	}
	bp, ok := memPool.pools[class].Get().(*[]byte)
	if !ok || bp == nil {
		// Type assertion failed, allocate new memory
		return make([]byte, size)
	}
	s := (*bp)[:size]
	clear(s)
	return s
}

// PutMemory returns a memory slice to the pool.
func PutMemory(b []byte) {
	class := sizeClass(cap(b))
	if class >= 0 && class < len(memPool.pools) {
		// Only return if the capacity matches the size class exactly
		if cap(b) == 1<<uint(class) {
			bp := b[:cap(b)]
			memPool.pools[class].Put(&bp)
		}
	}
}

