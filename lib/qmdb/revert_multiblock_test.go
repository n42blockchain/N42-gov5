// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import "testing"

// TestApplyUndoMultiBlockChain reverts a CHAIN of blocks newest→oldest through
// ApplyUndoWithStorage — the exact shape of the blockchain layer's
// branch-switch unwind loop. Live fire showed intermittent "frozen leaves are
// unrecoverable" failures only during MULTI-block unwinds under transaction
// load: the suspicion is that a later block's revert (which deletes truncated
// entry rows, drops tail-twig leaf blobs and rewrites the boundary twig)
// destroys persisted data that the NEXT (earlier) block's revert needs for its
// own boundary-twig reconstruction. Single-block coverage cannot see this.
func TestApplyUndoMultiBlockChain(t *testing.T) {
	store := make(mapStore)
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	// Base: enough blocks to freeze several twigs, all flushed + evicted.
	var flushed uint64
	for b := uint64(0); b < 6; b++ {
		rvApply(tr, rvBlockOps(b, 400))
		flushed = flushAndEvict(t, tr, store, flushed)
	}

	baseRoot := tr.Root()
	baseNext := tr.NextSlot()

	// Three consecutive blocks, each recorded, each flushed+evicted — heavy
	// enough (600 ops) that twig boundaries are crossed inside and between
	// blocks, so the boundary twig of one block's undo lies inside another
	// block's append range.
	type applied struct {
		undo *BlockUndo
		root Hash
		next uint64
	}
	var chain []applied
	for b := uint64(100); b < 103; b++ {
		undo := applyBlockRecorded(tr, rvBlockOps(b, 600))
		flushed = flushAndEvict(t, tr, store, flushed)
		chain = append(chain, applied{undo: undo, root: tr.Root(), next: tr.NextSlot()})
	}

	// Revert newest → oldest, exactly like unwindForReimport's loop.
	for i := len(chain) - 1; i >= 0; i-- {
		var err error
		flushed, err = tr.ApplyUndoWithStorage(store, chain[i].undo, flushed)
		if err != nil {
			t.Fatalf("multi-block revert failed at chain[%d] (newest-first index %d): %v",
				i, len(chain)-1-i, err)
		}
		// Intermediate roots must match the state right before that block.
		want := baseRoot
		wantNext := baseNext
		if i > 0 {
			want = chain[i-1].root
			wantNext = chain[i-1].next
		}
		if got := tr.Root(); got != want {
			t.Fatalf("root after reverting chain[%d] = %x, want %x", i, got[:8], want[:8])
		}
		if tr.NextSlot() != wantNext {
			t.Fatalf("cursor after reverting chain[%d] = %d, want %d", i, tr.NextSlot(), wantNext)
		}
	}

	// A fresh reload from the repaired store must reproduce the base root.
	reloaded := New()
	reloaded.SetCold(ColdReaderFromGetter(store))
	reloaded.SetLeafStore(LeafStoreFromGetter(store))
	if err := reloaded.LoadFrom(store); err != nil {
		t.Fatalf("reload after multi-block revert: %v", err)
	}
	if got := reloaded.Root(); got != baseRoot {
		t.Fatalf("reloaded root=%x want %x", got[:8], baseRoot[:8])
	}
}

// TestApplyUndoMultiBlockSharedTwig forces the tightest coupling: small blocks
// whose appends all land inside ONE shared twig, so every block's boundary
// reconstruction reads slots appended (and on revert, truncated) by its
// neighbors.
func TestApplyUndoMultiBlockSharedTwig(t *testing.T) {
	store := make(mapStore)
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	var flushed uint64
	rvApply(tr, rvBlockOps(0, 300))
	flushed = flushAndEvict(t, tr, store, flushed)

	baseRoot := tr.Root()
	baseNext := tr.NextSlot()

	type applied struct {
		undo *BlockUndo
		root Hash
		next uint64
	}
	var chain []applied
	for b := uint64(200); b < 206; b++ {
		undo := applyBlockRecorded(tr, rvBlockOps(b, 7)) // tiny: many blocks per twig
		flushed = flushAndEvict(t, tr, store, flushed)
		chain = append(chain, applied{undo: undo, root: tr.Root(), next: tr.NextSlot()})
	}

	for i := len(chain) - 1; i >= 0; i-- {
		var err error
		flushed, err = tr.ApplyUndoWithStorage(store, chain[i].undo, flushed)
		if err != nil {
			t.Fatalf("shared-twig revert failed at chain[%d]: %v", i, err)
		}
		want, wantNext := baseRoot, baseNext
		if i > 0 {
			want, wantNext = chain[i-1].root, chain[i-1].next
		}
		if got := tr.Root(); got != want {
			t.Fatalf("root after reverting chain[%d] = %x, want %x", i, got[:8], want[:8])
		}
		_ = wantNext
	}
}
