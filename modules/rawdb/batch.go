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

package rawdb

import (
	"sync"

	"github.com/ledgerwatch/erigon-lib/kv"
)

// BatchWriter provides efficient batch writing to the database.
type BatchWriter struct {
	tx      kv.RwTx
	pending int
	limit   int
	mu      sync.Mutex
}

// NewBatchWriter creates a new batch writer with the given transaction and flush limit.
func NewBatchWriter(tx kv.RwTx, flushLimit int) *BatchWriter {
	if flushLimit <= 0 {
		flushLimit = 10000 // Default batch size
	}
	return &BatchWriter{
		tx:    tx,
		limit: flushLimit,
	}
}

// Put adds a key-value pair to the batch.
func (b *BatchWriter) Put(bucket string, key, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.tx.Put(bucket, key, value); err != nil {
		return err
	}
	b.pending++
	return nil
}

// Delete removes a key from the batch.
func (b *BatchWriter) Delete(bucket string, key []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.tx.Delete(bucket, key); err != nil {
		return err
	}
	b.pending++
	return nil
}

// Pending returns the number of pending operations.
func (b *BatchWriter) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending
}

// Reset resets the pending counter.
func (b *BatchWriter) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = 0
}

// KeyBuffer provides a reusable key buffer pool for database operations.
type KeyBuffer struct {
	pool sync.Pool
	size int
}

// NewKeyBuffer creates a new key buffer pool.
func NewKeyBuffer(keySize int) *KeyBuffer {
	return &KeyBuffer{
		size: keySize,
		pool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, keySize)
				return &b
			},
		},
	}
}

// Get gets a key buffer from the pool.
func (kb *KeyBuffer) Get() []byte {
	return *kb.pool.Get().(*[]byte)
}

// Put returns a key buffer to the pool.
func (kb *KeyBuffer) Put(b []byte) {
	if cap(b) == kb.size {
		bp := b[:kb.size]
		kb.pool.Put(&bp)
	}
}

// Pre-allocated key buffers for common key sizes
var (
	// HeaderKeyBuffer for header keys (1 + 8 + 32 = 41 bytes)
	HeaderKeyBuffer = NewKeyBuffer(41)
	// BlockBodyKeyBuffer for block body keys (1 + 8 + 32 = 41 bytes)
	BlockBodyKeyBuffer = NewKeyBuffer(41)
	// TxLookupKeyBuffer for tx lookup keys (1 + 32 = 33 bytes)
	TxLookupKeyBuffer = NewKeyBuffer(33)
	// ReceiptKeyBuffer for receipt keys (1 + 8 = 9 bytes)
	ReceiptKeyBuffer = NewKeyBuffer(9)
	// BlockNumberKeyBuffer for block number encoding (8 bytes)
	BlockNumberKeyBuffer = NewKeyBuffer(8)
)

// ValueBuffer provides a reusable value buffer pool for database operations.
type ValueBuffer struct {
	pools []*sync.Pool
}

// Global value buffer pool with different size classes
var valueBufferPool = &ValueBuffer{
	pools: make([]*sync.Pool, 16), // 2^4 to 2^19 (16B to 512KB)
}

func init() {
	for i := range valueBufferPool.pools {
		size := 16 << uint(i) // Start at 16 bytes
		valueBufferPool.pools[i] = &sync.Pool{
			New: func() interface{} {
				b := make([]byte, size)
				return &b
			},
		}
	}
}

// sizeClass returns the pool index for a given size.
func valueSizeClass(size int) int {
	if size <= 16 {
		return 0
	}
	// Find the smallest power of 2 >= size, starting from 16
	class := 0
	s := (size - 1) >> 4 // Divide by 16
	for s > 0 {
		s >>= 1
		class++
	}
	if class >= len(valueBufferPool.pools) {
		return -1 // Too large for pool
	}
	return class
}

// GetValueBuffer gets a value buffer of at least the given size.
func GetValueBuffer(size int) []byte {
	class := valueSizeClass(size)
	if class < 0 {
		return make([]byte, size)
	}
	bp := valueBufferPool.pools[class].Get().(*[]byte)
	return (*bp)[:size]
}

// PutValueBuffer returns a value buffer to the pool.
func PutValueBuffer(b []byte) {
	class := valueSizeClass(cap(b))
	if class >= 0 && class < len(valueBufferPool.pools) {
		expectedSize := 16 << uint(class)
		if cap(b) == expectedSize {
			bp := b[:cap(b)]
			valueBufferPool.pools[class].Put(&bp)
		}
	}
}

