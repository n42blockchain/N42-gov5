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

package encoding

import (
	"bytes"
	"sync"
)

// BufferPool provides pooled bytes.Buffer instances to reduce allocations.
var BufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// GetBuffer retrieves a buffer from the pool.
func GetBuffer() *bytes.Buffer {
	buf := BufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// PutBuffer returns a buffer to the pool.
func PutBuffer(buf *bytes.Buffer) {
	// Don't return very large buffers to the pool
	if buf.Cap() > 64*1024 { // 64KB threshold
		return
	}
	buf.Reset()
	BufferPool.Put(buf)
}

// ByteSlicePool provides pooled byte slices of various sizes.
type ByteSlicePool struct {
	pools []*sync.Pool
}

// Global byte slice pool with different size classes
var byteSlicePool = &ByteSlicePool{
	pools: make([]*sync.Pool, 20), // 64B to 32MB
}

func init() {
	for i := range byteSlicePool.pools {
		size := 64 << uint(i) // Start at 64 bytes
		byteSlicePool.pools[i] = &sync.Pool{
			New: func() interface{} {
				b := make([]byte, size)
				return &b
			},
		}
	}
}

// sliceSizeClass returns the pool index for a given size.
func sliceSizeClass(size int) int {
	if size <= 64 {
		return 0
	}
	// Find the smallest power of 2 >= size, starting from 64
	class := 0
	s := (size - 1) >> 6 // Divide by 64
	for s > 0 {
		s >>= 1
		class++
	}
	if class >= len(byteSlicePool.pools) {
		return -1 // Too large for pool
	}
	return class
}

// GetByteSlice gets a byte slice of at least the given size.
func GetByteSlice(size int) []byte {
	class := sliceSizeClass(size)
	if class < 0 {
		return make([]byte, size)
	}
	bp := byteSlicePool.pools[class].Get().(*[]byte)
	return (*bp)[:size]
}

// PutByteSlice returns a byte slice to the pool.
func PutByteSlice(b []byte) {
	class := sliceSizeClass(cap(b))
	if class >= 0 && class < len(byteSlicePool.pools) {
		expectedSize := 64 << uint(class)
		if cap(b) == expectedSize {
			bp := b[:cap(b)]
			byteSlicePool.pools[class].Put(&bp)
		}
	}
}

// RLPEncoderPool provides pooled RLP encoders.
type RLPEncoderPool struct {
	pool sync.Pool
}

// EncoderBuffer is a reusable RLP encoding buffer.
type EncoderBuffer struct {
	buf *bytes.Buffer
}

// NewEncoderBuffer creates a new encoder buffer.
func NewEncoderBuffer() *EncoderBuffer {
	return &EncoderBuffer{
		buf: GetBuffer(),
	}
}

// Write writes bytes to the buffer.
func (e *EncoderBuffer) Write(b []byte) (int, error) {
	return e.buf.Write(b)
}

// WriteByte writes a single byte.
func (e *EncoderBuffer) WriteByte(b byte) error {
	return e.buf.WriteByte(b)
}

// Bytes returns the buffer contents.
func (e *EncoderBuffer) Bytes() []byte {
	return e.buf.Bytes()
}

// Len returns the buffer length.
func (e *EncoderBuffer) Len() int {
	return e.buf.Len()
}

// Reset clears the buffer.
func (e *EncoderBuffer) Reset() {
	e.buf.Reset()
}

// Release returns the buffer to the pool.
func (e *EncoderBuffer) Release() {
	PutBuffer(e.buf)
}

// Global RLP encoder pool
var rlpEncoderPool = &RLPEncoderPool{
	pool: sync.Pool{
		New: func() interface{} {
			return NewEncoderBuffer()
		},
	},
}

// GetRLPEncoder retrieves an RLP encoder from the pool.
func GetRLPEncoder() *EncoderBuffer {
	enc := rlpEncoderPool.pool.Get().(*EncoderBuffer)
	enc.Reset()
	return enc
}

// PutRLPEncoder returns an RLP encoder to the pool.
func PutRLPEncoder(enc *EncoderBuffer) {
	// Don't return very large buffers
	if enc.buf.Cap() > 64*1024 {
		enc.Release()
		return
	}
	enc.Reset()
	rlpEncoderPool.pool.Put(enc)
}

