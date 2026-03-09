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

import "sync"

const defaultBufSize = 4096

// bufPool provides reusable byte buffers for serialization hot paths.
// Using pooled buffers avoids per-call allocations and reduces GC pressure
// during block/receipt writes, which are frequent operations in chain sync.
var bufPool = &sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, defaultBufSize)
		return &b
	},
}

// GetBuf retrieves a reusable byte buffer from the pool.
// The returned buffer has length 0 but retains its previous capacity.
// Callers must return the buffer via PutBuf when finished.
func GetBuf() *[]byte {
	return bufPool.Get().(*[]byte)
}

// PutBuf returns a byte buffer to the pool for reuse.
// The buffer is reset to zero length but retains capacity.
// Buffers that have grown excessively large (>1MB) are discarded
// to prevent the pool from holding oversized allocations.
func PutBuf(b *[]byte) {
	if b == nil {
		return
	}
	// Discard oversized buffers to avoid holding excessive memory.
	if cap(*b) > 1<<20 {
		return
	}
	*b = (*b)[:0]
	bufPool.Put(b)
}
