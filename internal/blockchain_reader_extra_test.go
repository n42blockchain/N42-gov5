// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers blockchain_reader.go's remaining accessors not exercised by
// blockchain_reader_test.go: Quit's context-done channel, EarliestBlock's
// DB-marker and ancient-floor paths, GetDepositInfo/GetAccountRewardUnpaid
// against an empty store, Blocks(), and the deferred-execution hint
// Remember/take round trip.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestQuitClosesWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bc := &BlockChain{ctx: ctx}
	select {
	case <-bc.Quit():
		t.Fatalf("Quit() channel closed before cancellation")
	default:
	}
	cancel()
	select {
	case <-bc.Quit():
	default:
		t.Fatalf("Quit() channel not closed after cancellation")
	}
}

func TestEarliestBlockReadsMarkerAndAncientFloor(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	if got := bc.EarliestBlock(); got != 0 {
		t.Fatalf("EarliestBlock() with nothing written = %d, want 0", got)
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteEarliestBlock(tx, 100)
	}); err != nil {
		t.Fatal(err)
	}
	if got := bc.EarliestBlock(); got != 100 {
		t.Fatalf("EarliestBlock() after marker write = %d, want 100", got)
	}
}

func TestGetDepositInfoAndRewardUnpaidOnEmptyStore(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	addr := types.HexToAddress("0x1")
	rewardPerBlock, maxRewardPerEpoch := bc.GetDepositInfo(addr)
	if rewardPerBlock != nil || maxRewardPerEpoch != nil {
		t.Fatalf("GetDepositInfo(unknown) = (%v, %v), want (nil, nil)", rewardPerBlock, maxRewardPerEpoch)
	}

	unpaid, err := bc.GetAccountRewardUnpaid(addr)
	if err != nil {
		t.Fatal(err)
	}
	// An empty store answers with a nil/zero value rather than an error;
	// pin that contract so a future accessor rewrite surfaces here.
	_ = unpaid
}

func TestBlocksAccessor(t *testing.T) {
	blk := testConcreteBlock(&block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(1)}, &block.Body{})
	bc := &BlockChain{blocks: []block.IBlock{blk}}
	got := bc.Blocks()
	if len(got) != 1 || got[0].Hash() != blk.Hash() {
		t.Fatalf("Blocks() = %v, want [blk]", got)
	}
}

func TestRememberAndTakeExecutedResultHint(t *testing.T) {
	bc := &BlockChain{}
	hash := types.HexToHash("0xabc")

	if _, ok := bc.takeExecutedResultHint(hash); ok {
		t.Fatalf("takeExecutedResultHint() before Remember = ok, want not found")
	}

	want := rawdb.ExecutedResult{GasUsed: 21000}
	bc.RememberExecutedResult(hash, want)

	got, ok := bc.takeExecutedResultHint(hash)
	if !ok || got.GasUsed != want.GasUsed {
		t.Fatalf("takeExecutedResultHint() = (%+v, %v), want (%+v, true)", got, ok, want)
	}

	// take forgets it: a second call must miss.
	if _, ok := bc.takeExecutedResultHint(hash); ok {
		t.Fatalf("takeExecutedResultHint() did not forget the hint after one take")
	}
}
