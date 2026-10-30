// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package eldevp2p

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// GetReceipts is served from canonical blocks only, with the transaction
// types the receipts' network forms carry; a block without transactions
// has an empty list rather than none.
func TestChaindataProviderGetReceiptsByHash(t *testing.T) {
	db := memdb.NewTestDB(t)
	from := types.HexToAddress("0x1000000000000000000000000000000000000001")
	to := types.HexToAddress("0x2000000000000000000000000000000000000002")
	typed := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     2,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       30_000,
		To:        &to,
		Value:     uint256.NewInt(1),
	})
	txs := []*transaction.Transaction{
		transaction.NewTransaction(1, from, &to, uint256.NewInt(1), 21_000, uint256.NewInt(2), nil),
		typed,
	}
	newBlock := func(number uint64, txs []*transaction.Transaction, extra byte) *block.Block {
		return block.NewBlock(&block.Header{
			Number:     uint256.NewInt(number),
			Difficulty: uint256.NewInt(1),
			BaseFee:    uint256.NewInt(0),
			Extra:      []byte{extra},
		}, txs).(*block.Block)
	}
	full := newBlock(7, txs, 0)
	empty := newBlock(8, nil, 0)
	sibling := newBlock(7, txs, 1)
	receipts := block.Receipts{
		{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 21_000},
		{Status: block.ReceiptStatusFailed, CumulativeGasUsed: 45_000},
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, b := range []*block.Block{full, empty, sibling} {
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
		}
		if err := rawdb.WriteCanonicalHash(tx, full.Hash(), 7); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, empty.Hash(), 8); err != nil {
			return err
		}
		return rawdb.WriteReceipts(tx, 7, receipts)
	}); err != nil {
		t.Fatal(err)
	}

	p := &chaindataProvider{db: db}
	got, err := p.GetReceiptsByHash(full.Hash())
	if err != nil || len(got) != 2 {
		t.Fatalf("canonical block: %d receipts, err %v", len(got), err)
	}
	if got[0].Type != transaction.LegacyTxType || got[1].Type != transaction.DynamicFeeTxType {
		t.Fatalf("receipt types = %d, %d", got[0].Type, got[1].Type)
	}
	if got[1].Status != block.ReceiptStatusFailed || got[1].CumulativeGasUsed != 45_000 {
		t.Fatalf("second receipt = %+v", got[1])
	}

	got, err = p.GetReceiptsByHash(empty.Hash())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty block: %v, err %v", got, err)
	}

	// The receipts are stored by number: a non-canonical block at the same
	// height must not be served the canonical block's receipts.
	got, err = p.GetReceiptsByHash(sibling.Hash())
	if err != nil || got != nil {
		t.Fatalf("non-canonical block: %v, err %v", got, err)
	}
	got, err = p.GetReceiptsByHash(types.Hash{0xff})
	if err != nil || got != nil {
		t.Fatalf("unknown block: %v, err %v", got, err)
	}
}
