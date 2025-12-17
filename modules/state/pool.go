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

package state

import (
	"sync"

	"github.com/holiman/uint256"
)

// BalancePool provides pooled uint256.Int for balance operations.
var BalancePool = &sync.Pool{
	New: func() interface{} {
		return new(uint256.Int)
	},
}

// GetPooledBalance gets a balance uint256 from the pool.
func GetPooledBalance() *uint256.Int {
	return BalancePool.Get().(*uint256.Int)
}

// PutPooledBalance returns a balance uint256 to the pool.
func PutPooledBalance(b *uint256.Int) {
	if b != nil {
		b.Clear()
		BalancePool.Put(b)
	}
}

// StorageKeyPool provides pooled storage keys.
var StorageKeyPool = &sync.Pool{
	New: func() interface{} {
		return new([32]byte)
	},
}

// GetPooledStorageKey gets a storage key from the pool.
func GetPooledStorageKey() *[32]byte {
	return StorageKeyPool.Get().(*[32]byte)
}

// PutPooledStorageKey returns a storage key to the pool.
func PutPooledStorageKey(k *[32]byte) {
	if k != nil {
		*k = [32]byte{}
		StorageKeyPool.Put(k)
	}
}

// ByteSlicePool for code and data.
var ByteSlicePool = &sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 256)
		return &b
	},
}

// GetPooledByteSlice gets a byte slice from the pool.
func GetPooledByteSlice(size int) []byte {
	bp := ByteSlicePool.Get().(*[]byte)
	if cap(*bp) >= size {
		return (*bp)[:size]
	}
	// Return to pool and allocate new
	ByteSlicePool.Put(bp)
	return make([]byte, size)
}

// PutPooledByteSlice returns a byte slice to the pool.
func PutPooledByteSlice(b []byte) {
	if cap(b) >= 256 && cap(b) <= 4096 {
		bp := b[:0]
		ByteSlicePool.Put(&bp)
	}
}

// StoragePool provides pooled Storage maps.
var StoragePool = &sync.Pool{
	New: func() interface{} {
		return make(Storage)
	},
}

// GetPooledStorage gets a Storage map from the pool.
func GetPooledStorage() Storage {
	return StoragePool.Get().(Storage)
}

// PutPooledStorage returns a Storage map to the pool after clearing.
func PutPooledStorage(s Storage) {
	if s == nil {
		return
	}
	for k := range s {
		delete(s, k)
	}
	StoragePool.Put(s)
}
