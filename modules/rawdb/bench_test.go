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
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// benchHash is a shared hash used across all benchmarks.
var benchHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

// benchBytesSink keeps benchmark results alive to avoid compiler elimination.
var benchBytesSink atomic.Value

func init() {
	benchBytesSink.Store([]byte(nil))
}

// =============================================================================
// Key Generation Benchmarks
// =============================================================================

func BenchmarkHeaderKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)
	hash := benchHash
	var key []byte

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash[0] = byte(i)
		key = HeaderKey(number+uint64(i&1), hash)
	}
	benchBytesSink.Store(key)
}

func BenchmarkBlockBodyKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)
	hash := benchHash
	var key []byte

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash[0] = byte(i)
		key = BlockBodyKey(number+uint64(i&1), hash)
	}
	benchBytesSink.Store(key)
}

func BenchmarkTxLookupKeyGen(b *testing.B) {
	b.ReportAllocs()
	hash := benchHash
	var key []byte

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash[0] = byte(i)
		key = TxLookupKey(hash)
	}
	benchBytesSink.Store(key)
}

func BenchmarkReceiptKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)
	var key []byte

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key = ReceiptKey(number + uint64(i&1))
	}
	benchBytesSink.Store(key)
}

func BenchmarkEncodeBlockNumber(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)
	var key []byte

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key = EncodeBlockNumber(number + uint64(i&1))
	}
	benchBytesSink.Store(key)
}

// =============================================================================
// Parallel Benchmarks
// =============================================================================

func BenchmarkHeaderKeyParallel(b *testing.B) {
	number := uint64(12345)

	b.RunParallel(func(pb *testing.PB) {
		hash := benchHash
		var key []byte
		var i uint64
		for pb.Next() {
			hash[0] = byte(i)
			key = HeaderKey(number+(i&1), hash)
			i++
		}
		benchBytesSink.Store(key)
	})
}

func BenchmarkTxLookupKeyParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		hash := benchHash
		var key []byte
		var i uint64
		for pb.Next() {
			hash[0] = byte(i)
			key = TxLookupKey(hash)
			i++
		}
		benchBytesSink.Store(key)
	})
}

func BenchmarkEncodeBlockNumberParallel(b *testing.B) {
	number := uint64(12345)

	b.RunParallel(func(pb *testing.PB) {
		var key []byte
		var i uint64
		for pb.Next() {
			key = EncodeBlockNumber(number + (i & 1))
			i++
		}
		benchBytesSink.Store(key)
	})
}
