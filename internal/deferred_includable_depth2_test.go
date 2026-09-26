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

// guardChainDepth2 builds a 3-generation chain for the depth-2 includability
// check: grandparent G (its own stored execution result), parent P (its
// header carries G's result -- ordinary depth-1, unaffected by depth-2), and
// a child whose header is under test. depth2 controls whether
// DeferredExecutionDepth2Time is active at the child's own header time;
// correctReference controls whether the child's header carries G's result
// (right, under depth-2) or P's own executed result (wrong -- the depth-1
// pattern, used after depth-2 should have taken over).
func guardChainDepth2(t *testing.T, depth2 bool, correctReference bool) (bc *BlockChain, grandparent, parent, child *block.Header) {
	t.Helper()
	db := memdb.NewTestDB(t)
	rc := commitment.NewQMDBRootComputer()
	cfg := &params.ChainConfig{DeferredExecutionTime: bigInt(500)}
	if depth2 {
		cfg.DeferredExecutionDepth2Time = bigInt(1000)
	}
	bc = &BlockChain{
		ChainDB:          db,
		ctx:              context.Background(),
		chainConfig:      cfg,
		qmdbEnabled:      true,
		qmdbRootComputer: rc,
	}

	gExec := rawdb.ExecutedResult{Root: types.Hash{0xAA}, ReceiptHash: types.Hash{1}, GasUsed: 5}
	pExec := rawdb.ExecutedResult{Root: types.Hash{0xBB}, ReceiptHash: types.Hash{2}, GasUsed: 8}

	grandparent = &block.Header{Number: uint256.NewInt(10), Time: 998}
	parent = &block.Header{
		Number: uint256.NewInt(11), Time: 999, ParentHash: grandparent.Hash(),
		// Ordinary depth-1: P's header carries G's result.
		Root: gExec.Root, ReceiptHash: gExec.ReceiptHash, GasUsed: gExec.GasUsed,
	}
	child = &block.Header{Number: uint256.NewInt(12), Time: 1000, ParentHash: parent.Hash()}
	if correctReference {
		child.Root, child.ReceiptHash, child.GasUsed = gExec.Root, gExec.ReceiptHash, gExec.GasUsed
	} else {
		child.Root, child.ReceiptHash, child.GasUsed = pExec.Root, pExec.ReceiptHash, pExec.GasUsed
	}

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rawdb.WriteHeader(tx, grandparent)
	rawdb.WriteHeader(tx, parent)
	if err := rawdb.WriteExecutedResult(tx, grandparent.Hash(), gExec); err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteExecutedResult(tx, parent.Hash(), pExec); err != nil {
		t.Fatal(err)
	}
	// The tx-includability check always reads state AT THE PARENT (S55, 6f7
	// section 2b's own documented simplification), under both depths: mark
	// the parent applied so CheckDeferredBlock's own state-read precondition
	// is met.
	if err := rawdb.WriteQMDBApplied(tx, 11, parent.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return bc, grandparent, parent, child
}

// TestCheckDeferredBlockDepth2UsesGrandparentResult: with depth-2 active at
// the child's own header time, a header correctly carrying the GRANDPARENT's
// result (not the parent's) passes, and the reference ancestor returned is
// the grandparent's hash -- what deferredAttested (hotstuff package) needs.
func TestCheckDeferredBlockDepth2UsesGrandparentResult(t *testing.T) {
	bc, grandparent, _, child := guardChainDepth2(t, true, true)
	blk := block.NewBlock(child, nil)

	checked, retry, reference, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("checked=%v retry=%v err=%v", checked, retry, err)
	}
	if reference != grandparent.Hash() {
		t.Fatalf("reference = %x, want the grandparent %x", reference[:8], grandparent.Hash().Bytes()[:8])
	}
}

// TestCheckDeferredBlockDepth2RejectsParentPatternAfterActivation: once
// depth-2 is active, a header still following the depth-1 pattern (carrying
// the PARENT's own executed result) must be rejected -- the two rules are
// not interchangeable once the switch has crossed.
func TestCheckDeferredBlockDepth2RejectsParentPatternAfterActivation(t *testing.T) {
	bc, _, _, child := guardChainDepth2(t, true, false)
	blk := block.NewBlock(child, nil)

	checked, _, _, err := bc.CheckDeferredBlock(blk)
	if !checked {
		t.Fatal("a post-fork block must still be checked (and fail), not skipped")
	}
	if err == nil {
		t.Fatal("a header carrying the parent's result under active depth-2 must be rejected")
	}
	if errors.Is(err, ErrDeferredParentNotApplied) || errors.Is(err, ErrDeferredResultUnknown) {
		t.Fatalf("wrong error class: %v (want a header state-root mismatch)", err)
	}
}

// TestCheckDeferredBlockDepth2OffMatchesDepth1: the identical header/state
// fixture with depth-2 NOT active (before N42_DEFERRED_EXECUTION_DEPTH2_TIME)
// must validate under the ordinary depth-1 rule -- a header carrying the
// grandparent's result (not the parent's) is then WRONG, proving depth-1's
// own behaviour is untouched when the switch is off.
func TestCheckDeferredBlockDepth2OffMatchesDepth1(t *testing.T) {
	bc, _, _, child := guardChainDepth2(t, false, true) // "correct" for depth-2, wrong for depth-1
	blk := block.NewBlock(child, nil)

	checked, _, reference, err := bc.CheckDeferredBlock(blk)
	if !checked {
		t.Fatal("a post-fork (depth-1) block must be checked")
	}
	if err == nil {
		t.Fatal("depth-2 off: a header carrying the GRANDPARENT's result must be rejected (depth-1 wants the parent's)")
	}
	if reference != (types.Hash{}) {
		t.Fatalf("reference = %x, want zero when depth-2 is not active (the caller falls back to importedParents)", reference[:8])
	}
}

// TestCheckDeferredBlockDepth2FallsBackNearChainStart: depth-2 nominally
// active but the block is too early in the chain (number < 2) to have a
// grandparent -- must fall back to the depth-1 (parent) rule rather than
// underflowing or erroring, mirroring how depth-1 itself falls back to "the
// header's own fields" at genesis.
func TestCheckDeferredBlockDepth2FallsBackNearChainStart(t *testing.T) {
	db := memdb.NewTestDB(t)
	rc := commitment.NewQMDBRootComputer()
	bc := &BlockChain{
		ChainDB: db, ctx: context.Background(),
		chainConfig:      &params.ChainConfig{DeferredExecutionTime: bigInt(0), DeferredExecutionDepth2Time: bigInt(0)},
		qmdbEnabled:      true,
		qmdbRootComputer: rc,
	}
	genesis := &block.Header{Number: uint256.NewInt(0), Time: 0}
	first := &block.Header{Number: uint256.NewInt(1), Time: 1, ParentHash: genesis.Hash(),
		// Depth-1's own first-header rule: carries genesis's own fields.
		Root: genesis.Root, ReceiptHash: genesis.ReceiptHash, GasUsed: genesis.GasUsed,
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rawdb.WriteHeader(tx, genesis)
	if err := rawdb.WriteQMDBApplied(tx, 0, genesis.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	blk := block.NewBlock(first, nil)
	checked, retry, reference, err := bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("checked=%v retry=%v err=%v (block 1 has no grandparent; must fall back to depth-1)", checked, retry, err)
	}
	if reference != (types.Hash{}) {
		t.Fatalf("reference = %x, want zero (depth-1 fallback near chain start)", reference[:8])
	}
}
