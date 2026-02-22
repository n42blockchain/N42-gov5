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
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// benchHash is a shared hash used across all benchmarks.
var benchHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

// =============================================================================
// Key Generation Benchmarks
// =============================================================================

func BenchmarkHeaderKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HeaderKey(number, benchHash)
	}
}

func BenchmarkBlockBodyKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BlockBodyKey(number, benchHash)
	}
}

func BenchmarkTxLookupKeyGen(b *testing.B) {
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TxLookupKey(benchHash)
	}
}

func BenchmarkReceiptKeyGen(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReceiptKey(number)
	}
}

func BenchmarkEncodeBlockNumber(b *testing.B) {
	b.ReportAllocs()
	number := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeBlockNumber(number)
	}
}

// =============================================================================
// Parallel Benchmarks
// =============================================================================

func BenchmarkHeaderKeyParallel(b *testing.B) {
	number := uint64(12345)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			HeaderKey(number, benchHash)
		}
	})
}

func BenchmarkTxLookupKeyParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			TxLookupKey(benchHash)
		}
	})
}

func BenchmarkEncodeBlockNumberParallel(b *testing.B) {
	number := uint64(12345)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			EncodeBlockNumber(number)
		}
	})
}
