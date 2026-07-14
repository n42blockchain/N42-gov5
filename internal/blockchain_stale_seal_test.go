// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func TestWriteBlockWithStateRejectsStaleQMDBSealBeforeOtherWrites(t *testing.T) {
	db := newRealignTestDB(t)
	liveRoot := commitment.NewQMDBRootComputer()
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	bc.SetQMDBRootComputer(liveRoot)

	winningHash := types.Hash{0xAA}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteQMDBApplied(tx, 11, winningHash)
	}); err != nil {
		t.Fatal(err)
	}

	stale := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(11),
		ParentHash: types.Hash{0x10},
		Difficulty: uint256.NewInt(1),
	}, nil)
	ibs := state.New(nil)
	ibs.SetRootComputer(commitment.NewQMDBRootComputer()) // isolated miner tree

	_, err := bc.writeBlockWithState(stale, nil, ibs, nil)
	if !errors.Is(err, ErrStaleSeal) {
		t.Fatalf("writeBlockWithState() error = %v, want ErrStaleSeal", err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if td, err := rawdb.ReadTd(tx, stale.Hash(), stale.Number64().Uint64()); err != nil {
			return err
		} else if td != nil {
			t.Fatalf("stale seal wrote total difficulty: %s", td)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAlignAppliedBranchWaitsForChainMutationLock(t *testing.T) {
	bc := &BlockChain{}
	bc.lock.Lock()

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- bc.AlignAppliedBranch(1, types.Hash{0xAA})
	}()
	<-started

	select {
	case err := <-done:
		bc.lock.Unlock()
		t.Fatalf("AlignAppliedBranch returned before the mutation lock was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	bc.lock.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AlignAppliedBranch() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AlignAppliedBranch did not proceed after the mutation lock was released")
	}
}
