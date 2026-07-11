// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import "testing"

// TestLoadFromRoundTripFidelity: FlushTo → LoadFrom must reproduce the EXACT
// root of the tree that was flushed, across the shapes live nodes actually
// produce (multiple blocks, flush+evict cycles, an unwind, more blocks). Live
// fire showed every node's startup self-check reporting a tree root that
// matched NO nearby header after a graceful stop — i.e. the reload did not
// reproduce the pre-stop tree — with different nodes reloading DIFFERENT
// roots from logically identical chains.
func TestLoadFromRoundTripFidelity(t *testing.T) {
	store := make(mapStore)
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	var flushed uint64

	reloadAndCompare := func(stage string) {
		want := tr.Root()
		re := New()
		re.SetCold(ColdReaderFromGetter(store))
		re.SetLeafStore(LeafStoreFromGetter(store))
		if err := re.LoadFrom(store); err != nil {
			t.Fatalf("%s: LoadFrom: %v", stage, err)
		}
		if got := re.Root(); got != want {
			t.Fatalf("%s: reloaded root=%x, flushed tree root=%x", stage, got[:8], want[:8])
		}
		if re.NextSlot() != tr.NextSlot() {
			t.Fatalf("%s: reloaded nextSlot=%d want %d", stage, re.NextSlot(), tr.NextSlot())
		}
	}

	// Stage 1: plain growth with flush+evict each block.
	for b := uint64(0); b < 5; b++ {
		rvApply(tr, rvBlockOps(b, 500))
		flushed = flushAndEvict(t, tr, store, flushed)
	}
	reloadAndCompare("stage1-growth")

	// Stage 2: a recorded block, flushed, then reverted with storage fixups —
	// the branch-switch shape.
	undo := applyBlockRecorded(tr, rvBlockOps(50, 300))
	flushed = flushAndEvict(t, tr, store, flushed)
	var err error
	flushed, err = tr.ApplyUndoWithStorage(store, undo, flushed)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	reloadAndCompare("stage2-after-revert")

	// Stage 3: keep building on top of the reverted state.
	for b := uint64(60); b < 63; b++ {
		rvApply(tr, rvBlockOps(b, 200))
		flushed = flushAndEvict(t, tr, store, flushed)
	}
	reloadAndCompare("stage3-post-revert-growth")

	// Stage 4: deletes and overwrites of old keys (dead-slot churn).
	rvApply(tr, []rvOp{
		{key: rvKey(1), val: nil},
		{key: rvKey(2), val: []byte("overwrite")},
		{key: rvKey(3), val: nil},
	})
	flushed = flushAndEvict(t, tr, store, flushed)
	reloadAndCompare("stage4-churn")
}
