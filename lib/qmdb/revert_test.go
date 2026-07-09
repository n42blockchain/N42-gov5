// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the mutating live-tree revert (ApplyUndo): exact root round-trips
// and the sibling-switch equivalence that HotStuff same-height re-imports
// depend on (revert A then apply B == only ever applied B).

package qmdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

func rvKey(i uint64) Hash {
	var h Hash
	binary.BigEndian.PutUint64(h[:8], i)
	h[31] = 0x5a
	return h
}

func rvVal(tag string, i uint64) []byte {
	return []byte(fmt.Sprintf("%s-%d", tag, i))
}

// rvOp is one deterministic pseudo-random operation.
type rvOp struct {
	key Hash
	val []byte // nil = delete
}

// rvBlockOps builds block b's op list: a mix of fresh inserts, overwrites of
// earlier keys, and deletes — deterministic so two trees replaying the same
// blocks are identical.
func rvBlockOps(b uint64, opsPerBlock uint64) []rvOp {
	ops := make([]rvOp, 0, opsPerBlock)
	for j := uint64(0); j < opsPerBlock; j++ {
		n := b*opsPerBlock + j
		switch n % 5 {
		case 0, 1, 2: // fresh insert
			ops = append(ops, rvOp{key: rvKey(n), val: rvVal("v", n)})
		case 3: // overwrite an earlier key
			ops = append(ops, rvOp{key: rvKey(n / 2), val: rvVal("ow", n)})
		default: // delete an earlier key (no-op if already gone)
			ops = append(ops, rvOp{key: rvKey(n / 3), val: nil})
		}
	}
	return ops
}

func rvApply(t *Tree, ops []rvOp) {
	for _, o := range ops {
		if o.val == nil {
			t.Delete(o.key)
		} else {
			t.Set(o.key, o.val)
		}
	}
}

// applyBlockRecorded applies ops as one block with undo recording.
func applyBlockRecorded(t *Tree, ops []rvOp) *BlockUndo {
	t.StartUndoRecording()
	rvApply(t, ops)
	return t.StopUndoRecording()
}

// TestApplyUndoRootRoundTrip: apply N blocks recording roots+undos, then revert
// newest→oldest; every intermediate root must match byte-exactly, down to the
// pre-genesis empty root. Spans multiple twigs (>2048 appends).
func TestApplyUndoRootRoundTrip(t *testing.T) {
	tr := New()
	const blocks = 40
	const opsPerBlock = 80 // ~3200 appends total → crosses twig boundaries

	roots := make([]Hash, 0, blocks+1)
	undos := make([]*BlockUndo, 0, blocks)
	roots = append(roots, tr.Root())
	for b := uint64(0); b < blocks; b++ {
		undos = append(undos, applyBlockRecorded(tr, rvBlockOps(b, opsPerBlock)))
		roots = append(roots, tr.Root())
	}
	if tr.NextSlot() <= TwigSize {
		t.Fatalf("test did not cross a twig boundary: nextSlot=%d", tr.NextSlot())
	}

	for b := blocks - 1; b >= 0; b-- {
		if err := tr.ApplyUndo(undos[b]); err != nil {
			t.Fatalf("ApplyUndo block %d: %v", b, err)
		}
		got := tr.Root()
		if got != roots[b] {
			t.Fatalf("after reverting block %d: root=%x want %x", b, got[:8], roots[b][:8])
		}
	}
	if tr.NextSlot() != 0 {
		t.Fatalf("fully reverted tree has nextSlot=%d, want 0", tr.NextSlot())
	}
}

// TestApplyUndoSiblingEquivalence — the property the HotStuff sibling switch
// needs: (base + A + revert + B) must be indistinguishable from (base + B),
// both in root and in every key lookup.
func TestApplyUndoSiblingEquivalence(t *testing.T) {
	base := func() *Tree {
		tr := New()
		for b := uint64(0); b < 6; b++ {
			rvApply(tr, rvBlockOps(b, 100))
		}
		return tr
	}

	// Sibling blocks touch overlapping keys with different values.
	blockA := []rvOp{
		{key: rvKey(1), val: []byte("A-1")},
		{key: rvKey(2), val: nil},
		{key: rvKey(9001), val: []byte("A-new")},
		{key: rvKey(3), val: []byte("A-3")},
	}
	blockB := []rvOp{
		{key: rvKey(1), val: []byte("B-1")},
		{key: rvKey(5), val: nil},
		{key: rvKey(9002), val: []byte("B-new")},
	}

	t1 := base()
	undoA := applyBlockRecorded(t1, blockA)
	if err := t1.ApplyUndo(undoA); err != nil {
		t.Fatalf("revert A: %v", err)
	}
	rvApply(t1, blockB)

	t2 := base()
	rvApply(t2, blockB)

	r1, r2 := t1.Root(), t2.Root()
	if r1 != r2 {
		t.Fatalf("sibling switch diverged: root=%x want %x", r1[:8], r2[:8])
	}
	if t1.NextSlot() != t2.NextSlot() {
		t.Fatalf("cursor diverged: %d vs %d", t1.NextSlot(), t2.NextSlot())
	}
	for _, k := range []Hash{rvKey(1), rvKey(2), rvKey(3), rvKey(5), rvKey(9001), rvKey(9002)} {
		v1, ok1 := t1.Get(k)
		v2, ok2 := t2.Get(k)
		if ok1 != ok2 || !bytes.Equal(v1, v2) {
			t.Fatalf("key %x: (%q,%v) vs (%q,%v)", k[:8], v1, ok1, v2, ok2)
		}
	}
}

// TestApplyUndoAfterFlushEvict: revert must work when the block's rows were
// already flushed and evicted from RAM (the live-node shape: FlushTo +
// EvictThrough run every block), faulting truncated-slot reads from cold and
// restoring revived entries into the resident window.
func TestApplyUndoAfterFlushEvict(t *testing.T) {
	store := make(mapStore)
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	var flushed uint64
	for b := uint64(0); b < 6; b++ {
		rvApply(tr, rvBlockOps(b, 100))
		flushed = flushAndEvict(t, tr, store, flushed)
	}
	preRoot := tr.Root()
	preNext := tr.NextSlot()

	// rvKey(5) is inserted by rvBlockOps at n=5 and never touched again — a
	// real pre-existing key whose delete records a revival in the undo.
	if _, ok := tr.Get(rvKey(5)); !ok {
		t.Fatal("test precondition: rvKey(5) must exist in the base state")
	}
	undo := applyBlockRecorded(tr, []rvOp{
		{key: rvKey(1), val: []byte("head-1")},
		{key: rvKey(5), val: nil},
		{key: rvKey(7777), val: []byte("head-new")},
	})
	flushed = flushAndEvict(t, tr, store, flushed) // block persisted + evicted

	newFlushed, err := tr.ApplyUndoWithStorage(store, undo, flushed)
	if err != nil {
		t.Fatalf("ApplyUndoWithStorage after flush+evict: %v", err)
	}
	if newFlushed != preNext {
		t.Fatalf("flushed cursor after revert=%d want %d", newFlushed, preNext)
	}
	if got := tr.Root(); got != preRoot {
		t.Fatalf("root after revert=%x want %x", got[:8], preRoot[:8])
	}
	if tr.NextSlot() != preNext {
		t.Fatalf("cursor after revert=%d want %d", tr.NextSlot(), preNext)
	}
	// Revived + reverted keys must read their pre-block values again (the
	// revived slot's row was reclaimed by the block's flush — the storage
	// fixup re-puts it).
	if _, ok := tr.Get(rvKey(7777)); ok {
		t.Fatal("appended key still readable after revert")
	}
	if _, ok := tr.Get(rvKey(5)); !ok {
		t.Fatal("deleted key not revived after revert")
	}

	// A fresh reload from the repaired store must reproduce the reverted root
	// (standalone-revert consistency: meta/twig rows were rewritten).
	reloaded := New()
	reloaded.SetCold(ColdReaderFromGetter(store))
	reloaded.SetLeafStore(LeafStoreFromGetter(store))
	if err := reloaded.LoadFrom(store); err != nil {
		t.Fatalf("reload after revert: %v", err)
	}
	if got := reloaded.Root(); got != preRoot {
		t.Fatalf("reloaded root=%x want %x", got[:8], preRoot[:8])
	}
	if v, ok := reloaded.Get(rvKey(5)); !ok || len(v) == 0 {
		t.Fatal("revived key unreadable after reload")
	}
}

// TestApplyUndoPureDeleteBlock: a block that only deletes (no appends) reverts
// via revivals alone (PrevNextSlot == nextSlot).
func TestApplyUndoPureDeleteBlock(t *testing.T) {
	tr := New()
	rvApply(tr, rvBlockOps(0, 200))
	preRoot := tr.Root()

	undo := applyBlockRecorded(tr, []rvOp{
		{key: rvKey(0), val: nil},
		{key: rvKey(1), val: nil},
		{key: rvKey(2), val: nil},
	})
	if undo.PrevNextSlot != tr.NextSlot() {
		t.Fatalf("pure-delete block moved the cursor: %d -> %d", undo.PrevNextSlot, tr.NextSlot())
	}
	if err := tr.ApplyUndo(undo); err != nil {
		t.Fatalf("ApplyUndo: %v", err)
	}
	if got := tr.Root(); got != preRoot {
		t.Fatalf("root=%x want %x", got[:8], preRoot[:8])
	}
}

// TestApplyUndoRejectsBadInput: guards for the unsupported/invalid shapes.
func TestApplyUndoRejectsBadInput(t *testing.T) {
	tr := New()
	rvApply(tr, rvBlockOps(0, 10))

	if err := tr.ApplyUndo(nil); err == nil {
		t.Fatal("nil undo accepted")
	}
	if err := tr.ApplyUndo(&BlockUndo{PrevNextSlot: tr.NextSlot() + 1}); err == nil {
		t.Fatal("ahead-of-tree undo accepted")
	}
	if err := tr.ApplyUndo(&BlockUndo{PrevNextSlot: 0, broken: true}); err == nil {
		t.Fatal("poisoned undo accepted")
	}
	tr.StartUndoRecording()
	if err := tr.ApplyUndo(&BlockUndo{PrevNextSlot: tr.NextSlot()}); err == nil {
		t.Fatal("ApplyUndo during recording accepted")
	}
	tr.StopUndoRecording()
}
