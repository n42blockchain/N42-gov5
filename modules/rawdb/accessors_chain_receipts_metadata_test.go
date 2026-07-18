// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestReadReceiptsDerivesCanonicalMetadata(t *testing.T) {
	_, dbtx := memdb.NewTestTx(t)

	from := types.HexToAddress("0x1000000000000000000000000000000000000001")
	to := types.HexToAddress("0x2000000000000000000000000000000000000002")
	txs := []*transaction.Transaction{
		transaction.NewTransaction(1, from, &to, uint256.NewInt(1), 21_000, uint256.NewInt(2), nil),
		transaction.NewTransaction(2, from, &to, uint256.NewInt(2), 30_000, uint256.NewInt(2), nil),
	}
	header := &block.Header{
		Number:     uint256.NewInt(7),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}
	blk := block.NewBlock(header, txs).(*block.Block)
	wrongHash := types.HexToHash("0xdead")
	receipts := block.Receipts{
		{
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 21_000,
			TxHash:            wrongHash,
			Logs: []*block.Log{{
				Address:     to,
				BlockNumber: uint256.NewInt(99),
				TxHash:      wrongHash,
				BlockHash:   wrongHash,
				Index:       99,
				Removed:     true,
			}},
		},
		{
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 45_000,
			TxHash:            wrongHash,
			Logs: []*block.Log{
				{Address: to, BlockNumber: uint256.NewInt(99)},
				{Address: to, BlockNumber: uint256.NewInt(99)},
			},
		},
	}
	if err := WriteReceipts(dbtx, 7, receipts); err != nil {
		t.Fatalf("WriteReceipts: %v", err)
	}

	got := ReadReceipts(dbtx, blk, nil)
	if len(got) != 2 {
		t.Fatalf("receipt count = %d, want 2", len(got))
	}
	blockHash := blk.Hash()
	for i, receipt := range got {
		if receipt.TxHash != txs[i].Hash() {
			t.Fatalf("receipt %d tx hash = %s, want %s", i, receipt.TxHash, txs[i].Hash())
		}
		if receipt.BlockHash != blockHash || receipt.BlockNumber == nil || receipt.BlockNumber.Uint64() != 7 {
			t.Fatalf("receipt %d has wrong block metadata", i)
		}
		if receipt.TransactionIndex != uint(i) {
			t.Fatalf("receipt %d transaction index = %d", i, receipt.TransactionIndex)
		}
	}
	if got[0].GasUsed != 21_000 || got[1].GasUsed != 24_000 {
		t.Fatalf("gas used = [%d %d], want [21000 24000]", got[0].GasUsed, got[1].GasUsed)
	}

	wantLogIndex := uint(0)
	for txIndex, receipt := range got {
		for _, lg := range receipt.Logs {
			if lg.BlockNumber == nil || lg.BlockNumber.Uint64() != 7 || lg.BlockHash != blockHash {
				t.Fatalf("log %d has wrong block metadata", wantLogIndex)
			}
			if lg.TxHash != txs[txIndex].Hash() || lg.TxIndex != uint(txIndex) {
				t.Fatalf("log %d has wrong transaction metadata", wantLogIndex)
			}
			if lg.Index != wantLogIndex || lg.Removed {
				t.Fatalf("log index/removed = %d/%v, want %d/false", lg.Index, lg.Removed, wantLogIndex)
			}
			wantLogIndex++
		}
	}
}

func TestDeriveReceiptMetadataRejectsDecreasingCumulativeGas(t *testing.T) {
	tx := transaction.NewContractCreation(1, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	blk := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(8),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, []*transaction.Transaction{tx, tx}).(*block.Block)
	receipts := block.Receipts{
		{CumulativeGasUsed: 42_000},
		{CumulativeGasUsed: 21_000},
	}
	if deriveReceiptMetadata(receipts, blk, 8) {
		t.Fatal("accepted decreasing cumulative gas")
	}
}
