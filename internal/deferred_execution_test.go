// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"github.com/holiman/uint256"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// Across the fork: a pre-fork parent's result is its own header; a
// post-fork parent's is the stored result; the child header must carry it.
func TestDeferredHeaderCheckAcrossTheFork(t *testing.T) {
	cfg := &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// F-1: before the fork, executed under the old rule.
	preFork := &block.Header{Number: uint256.NewInt(10), Time: 999, Root: types.Hash{1}, ReceiptHash: types.Hash{2}, GasUsed: 7}
	preFork.Bloom[3] = 0x10
	got, err := ExecutedResultOfHeader(cfg, tx, preFork)
	if err != nil || got.Root != preFork.Root || got.ReceiptHash != preFork.ReceiptHash || got.Bloom != preFork.Bloom || got.GasUsed != 7 {
		t.Fatalf("pre-fork result should be the header's own fields: %+v err=%v", got, err)
	}
	// F: the first deferred header carries F-1's fields (the invariant).
	f := &block.Header{Number: uint256.NewInt(11), Time: 1000, ParentHash: preFork.Hash(), Root: preFork.Root, ReceiptHash: preFork.ReceiptHash, Bloom: preFork.Bloom, GasUsed: preFork.GasUsed}
	if err := checkDeferredHeader(cfg, tx, f, preFork); err != nil {
		t.Fatalf("fork header carrying the parent's own fields must pass: %v", err)
	}
	// F's own result is unknown until stored.
	if _, err := ExecutedResultOfHeader(cfg, tx, f); err == nil {
		t.Fatal("post-fork header without a stored result must be unknown")
	}
	fRes := rawdb.ExecutedResult{Root: types.Hash{0xF0}, ReceiptHash: types.Hash{0xF1}, GasUsed: 42}
	if err := rawdb.WriteExecutedResult(tx, f.Hash(), fRes); err != nil {
		t.Fatal(err)
	}
	// F+1 must carry F's stored result, not F's header fields.
	good := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: f.Hash(), Root: fRes.Root, ReceiptHash: fRes.ReceiptHash, GasUsed: 42}
	if err := checkDeferredHeader(cfg, tx, good, f); err != nil {
		t.Fatalf("child carrying the stored result must pass: %v", err)
	}
	bad := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: f.Hash(), Root: f.Root, ReceiptHash: f.ReceiptHash, GasUsed: f.GasUsed}
	if err := checkDeferredHeader(cfg, tx, bad, f); err == nil {
		t.Fatal("child carrying the parent's HEADER fields (not its result) must fail")
	}
	// Gas used alone off by one fails.
	off := *good
	off.GasUsed = 43
	if err := checkDeferredHeader(cfg, tx, &off, f); err == nil {
		t.Fatal("gas used mismatch must fail")
	}
}

func TestExecutedResultForUsesReceipts(t *testing.T) {
	receipts := []*block.Receipt{{CumulativeGasUsed: 21000}, {CumulativeGasUsed: 42000}}
	r := ExecutedResultFor(&params.ChainConfig{}, 5, types.Hash{7}, receipts)
	if r.GasUsed != 42000 || r.Root != (types.Hash{7}) {
		t.Fatalf("result %+v", r)
	}
	if r.ReceiptHash != ReceiptsRootFor(&params.ChainConfig{}, 5, receipts) {
		t.Fatal("receipts root must follow the validator's rule")
	}
}

func bigInt(v int64) *big.Int { return big.NewInt(v) }
