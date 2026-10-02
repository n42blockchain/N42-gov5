// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the worker's own-sealed bookkeeping (rememberSealed/ownSealed/
// firstSealedOnParent/recordSealedOnParent) and checkSealParentApplied's
// async-writer bypass, all with plain maps on a bare &worker{} -- no real
// blockchain (the *internal.BlockChain type assertion in rememberSealed
// simply fails gracefully and is skipped, which is itself the behavior a
// non-N42 chain implementation relies on).

package miner

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

func newTestWorkerForSealedBookkeeping() *worker {
	return &worker{
		engine:         apos.NewFaker(),
		pendingTasks:   make(map[types.Hash]*task),
		sealedOnParent: make(map[types.Hash]block.IBlock),
		sealedByHash:   make(map[types.Hash]block.IBlock),
		sealedPost:     make(map[types.Hash]*state.PostState),
		sealedExec:     make(map[types.Hash]rawdb.ExecutedResult),
	}
}

func TestRememberSealedAndOwnSealed(t *testing.T) {
	w := newTestWorkerForSealedBookkeeping()

	if w.ownSealed(types.Hash{0x01}) != nil {
		t.Fatalf("expected no sealed block before rememberSealed")
	}

	w.rememberSealed(nil) // nil guard, must not panic

	header := &block.Header{Number: uint256.NewInt(1)}
	blk := block.NewBlock(header, nil)
	w.rememberSealed(blk)

	got := w.ownSealed(blk.Hash())
	if got == nil || got.Hash() != blk.Hash() {
		t.Fatalf("expected rememberSealed to record the block by hash")
	}
}

func TestRememberSealedCarriesPendingTaskPostAndExec(t *testing.T) {
	w := newTestWorkerForSealedBookkeeping()

	header := &block.Header{Number: uint256.NewInt(1)}
	blk := block.NewBlock(header, nil)
	sealhash := w.engine.SealHash(blk.Header())

	post := &state.PostState{}
	exec := rawdb.ExecutedResult{}
	w.pendingTasks[sealhash] = &task{post: post, exec: &exec}

	w.rememberSealed(blk)

	if w.sealedPost[blk.Hash()] != post {
		t.Fatalf("expected the pending task's post-state carried into sealedPost")
	}
	if _, ok := w.sealedExec[blk.Hash()]; !ok {
		t.Fatalf("expected the pending task's exec result carried into sealedExec")
	}
}

func TestRememberSealedEvictsOldestPast16(t *testing.T) {
	w := newTestWorkerForSealedBookkeeping()

	for n := uint64(1); n <= 17; n++ {
		header := &block.Header{Number: uint256.NewInt(n), Extra: []byte{byte(n)}}
		w.rememberSealed(block.NewBlock(header, nil))
	}
	if len(w.sealedByHash) != 16 {
		t.Fatalf("expected sealedByHash bounded to 16 entries, got %d", len(w.sealedByHash))
	}
	// The oldest (number 1) must have been evicted.
	for _, b := range w.sealedByHash {
		if b.Number64().Uint64() == 1 {
			t.Fatalf("expected block number 1 to be evicted as the oldest")
		}
	}
}

func TestFirstSealedOnParentAndRecordSealedOnParent(t *testing.T) {
	w := newTestWorkerForSealedBookkeeping()
	parent := types.Hash{0x02}

	if w.firstSealedOnParent(parent) != nil {
		t.Fatalf("expected no recorded block before recordSealedOnParent")
	}

	header1 := &block.Header{Number: uint256.NewInt(1)}
	first := block.NewBlock(header1, nil)
	w.recordSealedOnParent(parent, first)

	// A second seal on the same parent must not replace the first (first
	// seal wins -- S26).
	header2 := &block.Header{Number: uint256.NewInt(1), Extra: []byte{0x99}}
	second := block.NewBlock(header2, nil)
	w.recordSealedOnParent(parent, second)

	got := w.firstSealedOnParent(parent)
	if got == nil || got.Hash() != first.Hash() {
		t.Fatalf("expected the first seal to win, got hash %v want %v", got.Hash(), first.Hash())
	}
}

func TestCheckSealParentAppliedDelegatesWithoutAsyncWriter(t *testing.T) {
	w := &worker{}
	header := &block.Header{Number: uint256.NewInt(1)}
	blk := block.NewBlock(header, nil)
	sentinel := errors.New("stale")
	c := &fakeSealParentChecker{wantErr: sentinel}

	err := w.checkSealParentApplied(c, blk, types.Hash{0x03})
	if err != sentinel {
		t.Fatalf("expected the real checker's error to propagate, got %v", err)
	}
	if !c.called {
		t.Fatalf("expected the real checker to be invoked with the block")
	}
}
