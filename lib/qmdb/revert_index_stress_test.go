// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Live-fire shape reproduction for the "undo record is poisoned (entry log /
// index mismatch at record time)" incident: a validator that interleaved
// hundreds of depth-1 sibling reverts (ApplyUndoWithStorage) with per-block
// FlushTo + aggressive entry eviction ended up with an in-memory index entry
// pointing at an unreadable slot — the NEXT block's Set hit it in
// recordDeactivation and poisoned that block's undo. The store reloaded from
// disk was healthy (LoadFrom rebuilds the index), so the desync lives strictly
// in the post-revert in-memory state.
//
// The stress loop below replays that macro-sequence deterministically and
// audits the FULL index -> entry-log mapping after every step, so the first
// divergence pinpoints the exact block/revert that planted the bad pointer.

package qmdb

import (
	"fmt"
	"testing"
)

// auditIndex walks every index mapping and requires it to point at a readable,
// live, key-matching entry — exactly the invariant recordDeactivation relies
// on ("live rows are never unreadable").
func auditIndex(t *testing.T, tr *Tree, tag string) {
	t.Helper()
	check := func(k Hash, slot uint64) {
		e, ok := tr.entryAt(slot)
		if !ok {
			t.Fatalf("%s: index maps %x -> slot %d but the entry is UNREADABLE (the poisoned-undo precondition)", tag, k[:8], slot)
		}
		if e.keyHash != k {
			t.Fatalf("%s: index maps %x -> slot %d but the entry there is for key %x", tag, k[:8], slot, e.keyHash[:8])
		}
		id := int(slot / TwigSize)
		if id >= len(tr.twigs) || !tr.twigs[id].bit(slot%TwigSize) {
			t.Fatalf("%s: index maps %x -> slot %d whose live bit is CLEAR", tag, k[:8], slot)
		}
	}
	switch idx := tr.idx.(type) {
	case mapIndex:
		for k, slot := range idx {
			check(k, slot)
		}
	case *flatIndex:
		for i := range idx.ctrl {
			if idx.ctrl[i]&0x80 != 0 { // occupied (fiFP sets the high bit)
				check(idx.keys[i], idx.slots[i])
			}
		}
	default:
		t.Fatalf("%s: unknown index type %T", tag, tr.idx)
	}
}

// stressOps derives block b's ops: a few fresh keys, overwrites of recent and
// of long-lived keys (the etherbase/system-contract shape: the same small key
// set is rewritten every block), and occasional deletes.
func stressOps(b uint64) []rvOp {
	ops := make([]rvOp, 0, 10)
	// Long-lived hot keys rewritten every block (reward/syscall shape).
	for h := uint64(0); h < 3; h++ {
		ops = append(ops, rvOp{key: rvKey(1_000_000 + h), val: rvVal("hot", b*10+h)})
	}
	// Fresh keys.
	for j := uint64(0); j < 4; j++ {
		n := b*100 + j
		ops = append(ops, rvOp{key: rvKey(n), val: rvVal("v", n)})
	}
	// Overwrite one recent key, delete an older one.
	if b > 2 {
		ops = append(ops, rvOp{key: rvKey((b-1)*100 + 1), val: rvVal("ow", b)})
		ops = append(ops, rvOp{key: rvKey((b-2)*100 + 2), val: nil})
	}
	return ops
}

// TestRevertIndexConsistencyUnderFlushEvict replays the incident macro-shape:
// per block -> record undo -> FlushTo -> EvictThrough(flushed), with a depth-1
// sibling revert (ApplyUndoWithStorage) every few blocks followed by the
// sibling's re-execution, auditing the whole index after every step.
func TestRevertIndexConsistencyUnderFlushEvict(t *testing.T) {
	tr := New()
	store := newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))

	flushed := uint64(0)
	var err error
	const blocks = 600

	evict := func() {
		tr.EvictThrough(flushed)      // entry window (production EvictFlushed…)
		tr.EvictTwigsThrough(flushed) // …evicts BOTH tiers
	}

	for b := uint64(1); b <= blocks; b++ {
		// Every 3rd block this node is the leader: it first executes a CANDIDATE
		// on the live tree (miner build), the candidate loses the view, and the
		// dangling appends are peeled with the memory-only ApplyUndo (no storage
		// fixups — the candidate was never flushed). This is the high-frequency
		// "sealed block dropped" path interleaving with everything else.
		if b%3 == 0 {
			candOps := append([]rvOp{}, stressOps(b)...)
			candOps = append(candOps, rvOp{key: rvKey(7_000_000 + b), val: rvVal("cand", b)})
			candUndo := applyBlockRecorded(tr, candOps)
			if candUndo.broken {
				t.Fatalf("block %d: candidate undo POISONED", b)
			}
			tr.Root()
			if err := tr.ApplyUndo(candUndo); err != nil {
				t.Fatalf("block %d: candidate peel: %v", b, err)
			}
			auditIndex(t, tr, fmt.Sprintf("after candidate peel at block %d", b))
		}

		ops := stressOps(b)
		undo := applyBlockRecorded(tr, ops)
		if undo.broken {
			t.Fatalf("block %d: undo POISONED during forward apply", b)
		}
		tr.Root()
		if flushed, _, err = tr.FlushTo(store, flushed); err != nil {
			t.Fatalf("block %d: flush: %v", b, err)
		}
		evict() // aggressive: everything flushed leaves RAM
		auditIndex(t, tr, fmt.Sprintf("after block %d", b))

		// Every 5th block: the block loses its view — revert it and apply the
		// competing sibling (slightly different ops) instead, like a HotStuff
		// same-height switch.
		if b%5 == 0 {
			if flushed, err = tr.ApplyUndoWithStorage(store, undo, flushed); err != nil {
				t.Fatalf("block %d: revert: %v", b, err)
			}
			auditIndex(t, tr, fmt.Sprintf("after revert of block %d", b))

			sibOps := append([]rvOp{}, ops...)
			sibOps = append(sibOps, rvOp{key: rvKey(9_000_000 + b), val: rvVal("sib", b)})
			sibUndo := applyBlockRecorded(tr, sibOps)
			if sibUndo.broken {
				t.Fatalf("block %d: sibling undo POISONED after revert — incident reproduced", b)
			}
			tr.Root()
			if flushed, _, err = tr.FlushTo(store, flushed); err != nil {
				t.Fatalf("block %d: sibling flush: %v", b, err)
			}
			evict()
			auditIndex(t, tr, fmt.Sprintf("after sibling of block %d", b))
		}
	}

	// End-to-end cross-check: a fresh tree reloaded from the store must agree
	// with the live tree (root and index size).
	fresh := New()
	fresh.SetCold(ColdReaderFromGetter(store))
	if err := fresh.LoadFrom(store); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.Root() != tr.Root() {
		t.Fatalf("reloaded root %x != live root %x (disk/RAM divergence)", fresh.Root(), tr.Root())
	}
	auditIndex(t, fresh, "reloaded tree")
}
