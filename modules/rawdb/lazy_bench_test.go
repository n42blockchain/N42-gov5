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

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// benchReceipt creates a realistic receipt for benchmarking.
func benchReceipt() *block.Receipt {
	return &block.Receipt{
		Status:            block.ReceiptStatusSuccessful,
		GasUsed:           21000,
		CumulativeGasUsed: 42000,
		TxHash:            types.HexToHash("0xaaa"),
		BlockNumber:       uint256.NewInt(100),
		Logs: []*block.Log{
			{
				Address:     types.HexToAddress("0xbbb"),
				Topics:      []types.Hash{types.HexToHash("0xccc"), types.HexToHash("0xddd")},
				Data:        make([]byte, 256),
				BlockNumber: uint256.NewInt(100),
			},
			{
				Address:     types.HexToAddress("0xeee"),
				Topics:      []types.Hash{types.HexToHash("0xfff")},
				Data:        make([]byte, 128),
				BlockNumber: uint256.NewInt(100),
			},
		},
	}
}

// BenchmarkLazyReceipt_StatusOnly measures the cost of reading only the Status
// field via lazy parsing vs performing a full Receipt deserialization.
func BenchmarkLazyReceipt_StatusOnly(b *testing.B) {
	r := benchReceipt()
	raw, err := r.Marshal()
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Lazy", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lr := NewLazyReceipt(raw)
			_ = lr.Status()
		}
	})

	b.Run("FullParse", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var receipt block.Receipt
			if err := receipt.Unmarshal(raw); err != nil {
				b.Fatal(err)
			}
			_ = receipt.Status
		}
	})
}

// BenchmarkBufPool_VsAlloc measures the allocation savings of using the
// buffer pool compared to fresh allocations.
func BenchmarkBufPool_VsAlloc(b *testing.B) {
	b.Run("Pooled", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf := GetBuf()
			*buf = append(*buf, make([]byte, 1024)...)
			PutBuf(buf)
		}
	})

	b.Run("FreshAlloc", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf := make([]byte, 0, defaultBufSize)
			buf = append(buf, make([]byte, 1024)...)
			_ = buf
		}
	})
}

// BenchmarkReadReceiptsBatch_VsIndividual compares batch cursor-based reading
// against individual GetOne reads for sequential block receipts.
func BenchmarkReadReceiptsBatch_VsIndividual(b *testing.B) {
	_, tx := memdb.NewTestTx(b)

	const numBlocks = 100
	for i := uint64(0); i < numBlocks; i++ {
		receipts := block.Receipts{benchReceipt()}
		receipts[0].BlockNumber = uint256.NewInt(i)
		if err := WriteReceipts(tx, i, receipts); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("Batch", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := ReadReceiptsBatch(tx, 0, numBlocks-1)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Individual", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := uint64(0); j < numBlocks; j++ {
				_ = ReadRawReceipts(tx, j)
			}
		}
	})
}

// BenchmarkLazyReceipt_FullParse measures the overhead of LazyReceipt.Full()
// compared to direct Unmarshal.
func BenchmarkLazyReceipt_FullParse(b *testing.B) {
	r := benchReceipt()
	raw, err := r.Marshal()
	if err != nil {
		b.Fatal(err)
	}

	b.Run("LazyFull", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lr := NewLazyReceipt(raw)
			_ = lr.Full()
		}
	})

	b.Run("DirectUnmarshal", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var receipt block.Receipt
			_ = receipt.Unmarshal(raw)
		}
	})
}
