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

// writeTestReceipts writes receipts for the given block numbers into the DB.
func writeTestReceipts(t *testing.T, tx interface {
	Put(table string, k, v []byte) error
}, blockNums ...uint64) {
	t.Helper()
	for _, num := range blockNums {
		receipts := block.Receipts{
			{
				Status:            block.ReceiptStatusSuccessful,
				GasUsed:           21000 * (num + 1),
				CumulativeGasUsed: 21000 * (num + 1),
				BlockNumber:       uint256.NewInt(num),
			},
		}
		if err := WriteReceipts(tx, num, receipts); err != nil {
			t.Fatalf("WriteReceipts at block %d failed: %v", num, err)
		}
	}
}

func TestReadReceiptsBatch(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Write receipts for blocks 10, 11, 12, 13, 14.
	writeTestReceipts(t, tx, 10, 11, 12, 13, 14)

	// Read range [10, 14].
	result, err := ReadReceiptsBatch(tx, 10, 14)
	if err != nil {
		t.Fatalf("ReadReceiptsBatch failed: %v", err)
	}
	if len(result) != 5 {
		t.Fatalf("expected 5 blocks, got %d", len(result))
	}

	for num := uint64(10); num <= 14; num++ {
		rs, ok := result[num]
		if !ok {
			t.Fatalf("block %d missing from result", num)
		}
		if len(rs) != 1 {
			t.Fatalf("block %d: expected 1 receipt, got %d", num, len(rs))
		}
		expectedGas := 21000 * (num + 1)
		if rs[0].GasUsed != expectedGas {
			t.Fatalf("block %d: expected GasUsed %d, got %d", num, expectedGas, rs[0].GasUsed)
		}
	}
}

func TestReadReceiptsBatch_SubRange(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	writeTestReceipts(t, tx, 10, 11, 12, 13, 14)

	// Read only blocks [11, 13].
	result, err := ReadReceiptsBatch(tx, 11, 13)
	if err != nil {
		t.Fatalf("ReadReceiptsBatch failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(result))
	}
	if _, ok := result[10]; ok {
		t.Fatal("block 10 should not be in [11,13] result")
	}
	if _, ok := result[14]; ok {
		t.Fatal("block 14 should not be in [11,13] result")
	}
}

func TestReadReceiptsBatch_Empty(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Read range with no data.
	result, err := ReadReceiptsBatch(tx, 100, 200)
	if err != nil {
		t.Fatalf("ReadReceiptsBatch failed: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(result))
	}
}

func TestReadReceiptsBatch_InvalidRange(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	_, err := ReadReceiptsBatch(tx, 200, 100)
	if err == nil {
		t.Fatal("expected error for fromBlock > toBlock")
	}
}

func TestReadHeadersBatch(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Write headers for blocks 5, 6, 7.
	for num := uint64(5); num <= 7; num++ {
		header := &block.Header{
			Number:     uint256.NewInt(num),
			GasLimit:   8000000,
			GasUsed:    uint64(num * 1000),
			Difficulty: uint256.NewInt(1),
			BaseFee:    uint256.NewInt(0),
		}
		hash := header.Hash()
		WriteHeader(tx, header)
		if err := WriteCanonicalHash(tx, hash, num); err != nil {
			t.Fatalf("WriteCanonicalHash failed: %v", err)
		}
	}

	result, err := ReadHeadersBatch(tx, 5, 7)
	if err != nil {
		t.Fatalf("ReadHeadersBatch failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 headers, got %d", len(result))
	}

	for num := uint64(5); num <= 7; num++ {
		h, ok := result[num]
		if !ok {
			t.Fatalf("block %d missing from result", num)
		}
		if h.Number.Uint64() != num {
			t.Fatalf("block %d: number mismatch, got %d", num, h.Number.Uint64())
		}
		if h.GasUsed != num*1000 {
			t.Fatalf("block %d: GasUsed mismatch, got %d", num, h.GasUsed)
		}
	}
}

func TestReadHeadersBatch_NonCanonicalSkipped(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Write a header at block 10.
	header := &block.Header{
		Number:     uint256.NewInt(10),
		GasLimit:   8000000,
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}
	WriteHeader(tx, header)

	// Write a DIFFERENT canonical hash for block 10 so the header doesn't match.
	fakeHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if err := WriteCanonicalHash(tx, fakeHash, 10); err != nil {
		t.Fatalf("WriteCanonicalHash failed: %v", err)
	}

	result, err := ReadHeadersBatch(tx, 10, 10)
	if err != nil {
		t.Fatalf("ReadHeadersBatch failed: %v", err)
	}
	// The header's hash doesn't match the canonical hash, so it should be skipped.
	if len(result) != 0 {
		t.Fatalf("expected 0 headers (non-canonical should be skipped), got %d", len(result))
	}
}

func TestReadHeadersBatch_Empty(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	result, err := ReadHeadersBatch(tx, 100, 200)
	if err != nil {
		t.Fatalf("ReadHeadersBatch failed: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(result))
	}
}

func TestReadHeadersBatch_InvalidRange(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	_, err := ReadHeadersBatch(tx, 200, 100)
	if err == nil {
		t.Fatal("expected error for fromBlock > toBlock")
	}
}
