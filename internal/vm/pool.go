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

import (
	"sync"

	"github.com/holiman/uint256"
)

// Uint256Pool is a pool of *uint256.Int to reduce allocations in hot paths.
var Uint256Pool = &sync.Pool{
	New: func() interface{} {
		return new(uint256.Int)
	},
}

// GetUint256 gets a *uint256.Int from the pool.
func GetUint256() *uint256.Int {
	return Uint256Pool.Get().(*uint256.Int)
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
	New: func() interface{} {
		// Default to 32 bytes (common size for words)
		b := make([]byte, 32)
		return &b
	},
}

// GetByteSlice gets a byte slice from the pool with at least the given capacity.
func GetByteSlice(size int) []byte {
	if size <= 32 {
		bp := ByteSlicePool.Get().(*[]byte)
		return (*bp)[:size]
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
	New: func() interface{} {
		b := make([]byte, 32)
		return &b
	},
}

// GetHashBuffer gets a 32-byte buffer from the pool.
func GetHashBuffer() *[]byte {
	return HashPool.Get().(*[]byte)
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
			New: func() interface{} {
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

// GetMemory gets a memory slice of at least the given size.
func GetMemory(size int) []byte {
	class := sizeClass(size)
	if class < 0 {
		return make([]byte, size)
	}
	bp := memPool.pools[class].Get().(*[]byte)
	return (*bp)[:size]
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

