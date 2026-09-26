// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// guardChain builds the smallest chain the includability check needs: a
// pre-fork parent (so its executed result is its own header), the first
// deferred child carrying it, and a QMDB computer standing in for the live
// tree.
func guardChain(t *testing.T, atTree bool) (*BlockChain, *block.Header, *block.Header) {
	t.Helper()
	db := memdb.NewTestDB(t)
	rc := commitment.NewQMDBRootComputer()
	bc := &BlockChain{
		ChainDB:          db,
		ctx:              context.Background(),
		chainConfig:      &params.ChainConfig{DeferredExecutionTime: bigInt(1000)},
		qmdbEnabled:      true,
		qmdbRootComputer: rc,
	}
	// The parent's executed root: the tree's own root, or one it is not at.
	parentRoot := types.Hash{0xAB}
	if atTree {
		parentRoot = rc.Root()
	}
	parent := &block.Header{Number: uint256.NewInt(10), Time: 999, Root: parentRoot, ReceiptHash: types.Hash{2}, GasUsed: 7}
	child := &block.Header{
		Number: uint256.NewInt(11), Time: 1000, ParentHash: parent.Hash(),
		Root: parent.Root, ReceiptHash: parent.ReceiptHash, Bloom: parent.Bloom, GasUsed: parent.GasUsed,
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rawdb.WriteHeader(tx, parent)
	if err := rawdb.WriteQMDBApplied(tx, 10, parent.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return bc, parent, child
}

// The tree must be at the parent's post-state before the check reads senders.
// The applied marker moves after the tree takes a block's appends, so the
// marker alone is not that proof; under deferred execution the header carries
// the root, and a tree that has moved on asks for a retry, not a failure
// (35zzq: a follower failed a valid block "nonce 0, state expects 4096").
func TestDeferredCheckRetriesWhenTheTreeHasMovedOn(t *testing.T) {
	bc, _, child := guardChain(t, false)
	blk := block.NewBlock(child, nil)

	checked, retry, _, err := bc.CheckDeferredBlock(blk)
	if !checked {
		t.Fatal("a post-fork block must be checked")
	}
	if !retry || err == nil {
		t.Fatalf("a tree that is not at the parent's post-state must retry: retry=%v err=%v", retry, err)
	}
	if !errors.Is(err, ErrDeferredParentNotApplied) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// With the tree at the parent's post-state and no transactions to weigh, the
// check passes.
func TestDeferredCheckPassesWithTheTreeAtTheParent(t *testing.T) {
	bc, _, child := guardChain(t, true)
	blk := block.NewBlock(child, nil)

	checked, retry, _, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("checked=%v retry=%v err=%v", checked, retry, err)
	}
}

// A block this node has already applied is not re-checked: its senders' state
// is the block's own post-state, where its transactions read as spent nonces.
func TestDeferredCheckSkipsABlockAlreadyApplied(t *testing.T) {
	bc, _, child := guardChain(t, false) // the tree has moved on, as it has after applying
	blk := block.NewBlock(child, nil)

	tx, err := bc.ChainDB.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteQMDBApplied(tx, 11, blk.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	checked, retry, _, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("an applied block must pass without a state read: checked=%v retry=%v err=%v", checked, retry, err)
	}
}
