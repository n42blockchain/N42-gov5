// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Round 35z2 (2026-09-08): under leader tenure the leader applied its own
// sibling X at height h, was switched off it by an incoming sibling Y, then
// converged back to X (lowest hash) and re-applied it. Its live tree then
// produced a root for h+1 that the six followers -- who had applied Y, unwound
// it and applied X -- did not reproduce, and that the leader's own speculative
// tree did not reproduce either. Three roots for one block. This pins the
// invariant both paths rely on: after a storage-backed revert, applying a
// block yields the same root as applying it on the original base.

package qmdb

import (
	"fmt"
	"testing"
)

// flushEvictHere flushes the tree to store and evicts everything flushed, the
// per-block production shape.
func flushEvictHere(t *testing.T, tr *Tree, store mapStore, flushed uint64) uint64 {
	t.Helper()
	var err error
	if flushed, _, err = tr.FlushTo(store, flushed); err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr.EvictThrough(flushed)
	tr.EvictTwigsThrough(flushed)
	return flushed
}

// baseTree builds a flushed, evicted tree of `blocks` blocks.
func baseTree(t *testing.T, blocks uint64) (*Tree, mapStore, uint64) {
	t.Helper()
	tr := New()
	store := newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))
	flushed := uint64(0)
	for b := uint64(1); b <= blocks; b++ {
		applyBlockRecorded(tr, stressOps(b))
		tr.Root()
		flushed = flushEvictHere(t, tr, store, flushed)
	}
	return tr, store, flushed
}

func TestRevertThenReapplySameBlockRoot(t *testing.T) {
	for _, base := range []uint64{5, 50, 700} {
		t.Run(fmt.Sprintf("base%d", base), func(t *testing.T) {
			h := base + 1
			xOps := stressOps(h)
			yOps := append([]rvOp{}, stressOps(h)...)
			yOps = append(yOps, rvOp{key: rvKey(9_000_000 + h), val: rvVal("sib", h)})
			nextOps := stressOps(h + 1)

			// Reference: fresh apply of X then h+1 on the base.
			ref, _, _ := baseTree(t, base)
			applyBlockRecorded(ref, xOps)
			rootX := ref.Root()
			applyBlockRecorded(ref, nextOps)
			rootNext := ref.Root()

			// Leader path: apply X, flush+evict, revert X (storage-backed),
			// re-apply X, then h+1.
			tr, store, flushed := baseTree(t, base)
			undoX := applyBlockRecorded(tr, xOps)
			if got := tr.Root(); got != rootX {
				t.Fatalf("leader: first apply of X root %x != reference %x", got[:8], rootX[:8])
			}
			flushed = flushEvictHere(t, tr, store, flushed)
			var err error
			if flushed, err = tr.ApplyUndoWithStorage(store, undoX, flushed); err != nil {
				t.Fatalf("leader: revert X: %v", err)
			}
			applyBlockRecorded(tr, xOps)
			if got := tr.Root(); got != rootX {
				t.Fatalf("leader: re-apply of X after its own revert: root %x != %x (the 35z2 leader shape)", got[:8], rootX[:8])
			}
			flushed = flushEvictHere(t, tr, store, flushed)
			applyBlockRecorded(tr, nextOps)
			if got := tr.Root(); got != rootNext {
				t.Fatalf("leader: block h+1 after revert/re-apply: root %x != %x", got[:8], rootNext[:8])
			}

			// Follower path: apply Y, flush+evict, revert Y, apply X, then h+1.
			fo, fstore, fflushed := baseTree(t, base)
			undoY := applyBlockRecorded(fo, yOps)
			fo.Root()
			fflushed = flushEvictHere(t, fo, fstore, fflushed)
			if fflushed, err = fo.ApplyUndoWithStorage(fstore, undoY, fflushed); err != nil {
				t.Fatalf("follower: revert Y: %v", err)
			}
			applyBlockRecorded(fo, xOps)
			if got := fo.Root(); got != rootX {
				t.Fatalf("follower: X after reverting sibling Y: root %x != %x", got[:8], rootX[:8])
			}
			fflushed = flushEvictHere(t, fo, fstore, fflushed)
			applyBlockRecorded(fo, nextOps)
			if got := fo.Root(); got != rootNext {
				t.Fatalf("follower: block h+1: root %x != %x", got[:8], rootNext[:8])
			}

			// And a reload of each store must agree with its live tree.
			for name, pair := range map[string]struct {
				tr *Tree
				st mapStore
			}{"leader": {tr, store}, "follower": {fo, fstore}} {
				fresh := New()
				fresh.SetCold(ColdReaderFromGetter(pair.st))
				if err := fresh.LoadFrom(pair.st); err != nil {
					t.Fatalf("%s reload: %v", name, err)
				}
				// The live tree has h+1 unflushed; compare after flushing it.
				flushEvictHere(t, pair.tr, pair.st, 0)
				fresh2 := New()
				fresh2.SetCold(ColdReaderFromGetter(pair.st))
				if err := fresh2.LoadFrom(pair.st); err != nil {
					t.Fatalf("%s reload2: %v", name, err)
				}
				if fresh2.Root() != pair.tr.Root() {
					t.Fatalf("%s: reloaded root %x != live %x", name, fresh2.Root(), pair.tr.Root())
				}
				_ = fresh
			}
		})
	}
}
