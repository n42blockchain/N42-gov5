// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the trusted-index fast reload (the persistent miner computer's
// path) and for the deadFlushed-carryover fix — the root cause behind the
// fleet-wide "ghost live bits" (live slots whose entry rows were deleted),
// which in turn was the physical precondition of the poisoned-undo incident.

package qmdb

import (
	"errors"
	"testing"
)

// buildFlushedTree writes `blocks` blocks of ops into a fresh tree, flushing
// (and evicting) after every block — the live store shape.
func buildFlushedTree(t *testing.T, store mapStore, blocks uint64) (*Tree, uint64) {
	t.Helper()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	flushed := uint64(0)
	var err error
	for b := uint64(1); b <= blocks; b++ {
		rvApply(tr, stressOps(b))
		tr.Root()
		if flushed, _, err = tr.FlushTo(store, flushed); err != nil {
			t.Fatalf("block %d flush: %v", b, err)
		}
		tr.EvictThrough(flushed)
		tr.EvictTwigsThrough(flushed)
	}
	return tr, flushed
}

// TestTrustedIndexReloadMatchesFullRebuild: after a candidate build is peeled,
// a trusted-index reload must land on the exact same root and index population
// as a from-scratch rebuild — at O(twig meta) cost.
func TestTrustedIndexReloadMatchesFullRebuild(t *testing.T) {
	store := newMapStore()
	live, _ := buildFlushedTree(t, store, 30)
	wantRoot := live.Root()

	// Persistent "miner" tree: full load once...
	miner := New()
	miner.SetCold(ColdReaderFromGetter(store))
	if err := miner.LoadFrom(store); err != nil {
		t.Fatalf("full load: %v", err)
	}
	baseDelta := miner.LiveBits() - miner.LiveCount()
	prevCursor := miner.NextSlot()

	// ...then a speculative candidate is built and peeled...
	undo := applyBlockRecorded(miner, stressOps(99))
	miner.Root()
	if err := miner.ApplyUndo(undo); err != nil {
		t.Fatalf("candidate peel: %v", err)
	}

	// ...and the trusted reload must reproduce the store exactly.
	if err := miner.LoadFromTrustedIndex(store, prevCursor, baseDelta); err != nil {
		t.Fatalf("trusted reload: %v", err)
	}
	if got := miner.Root(); got != wantRoot {
		t.Fatalf("trusted reload root %x != live root %x", got, wantRoot)
	}
	if miner.LiveCount() != live.LiveCount() {
		t.Fatalf("trusted reload index size %d != live %d", miner.LiveCount(), live.LiveCount())
	}
	auditIndex(t, miner, "after trusted reload")
}

// TestTrustedIndexReloadScansDelta: appends flushed AFTER the trust boundary
// must land in the index during a trusted reload.
func TestTrustedIndexReloadScansDelta(t *testing.T) {
	store := newMapStore()
	live, flushed := buildFlushedTree(t, store, 20)

	miner := New()
	miner.SetCold(ColdReaderFromGetter(store))
	if err := miner.LoadFrom(store); err != nil {
		t.Fatalf("full load: %v", err)
	}
	baseDelta := miner.LiveBits() - miner.LiveCount()
	prevCursor := miner.NextSlot()

	// The chain advances two more blocks: fresh keys plus overwrites of old
	// (trusted-zone) ones — the dominant live shape. No deletes here; an
	// in-window DELETE leaves a dead key in the trusted index that no rescan
	// can observe, and reconciliation must (and does, see below) reject it.
	var err error
	for b := uint64(21); b <= 22; b++ {
		ops := []rvOp{
			{key: rvKey(5_000_000 + b), val: rvVal("fresh", b)},
			{key: rvKey(1_000_000), val: rvVal("hot", b)}, // overwrite a trusted-zone key
		}
		rvApply(live, ops)
		live.Root()
		if flushed, _, err = live.FlushTo(store, flushed); err != nil {
			t.Fatalf("block %d flush: %v", b, err)
		}
	}

	if err := miner.LoadFromTrustedIndex(store, prevCursor, baseDelta); err != nil {
		t.Fatalf("trusted reload across delta: %v", err)
	}
	if got, want := miner.Root(), live.Root(); got != want {
		t.Fatalf("delta reload root %x != live %x", got, want)
	}
	auditIndex(t, miner, "after delta reload")

	// An in-window DELETE must fail reconciliation (dead key retained in the
	// trusted index) and push the caller to the full rebuild.
	prevCursor = miner.NextSlot()
	live.Delete(rvKey(1_000_000))
	live.Root()
	if flushed, _, err = live.FlushTo(store, flushed); err != nil {
		t.Fatalf("delete flush: %v", err)
	}
	if err := miner.LoadFromTrustedIndex(store, prevCursor, baseDelta); !errors.Is(err, ErrIndexReconcile) {
		t.Fatalf("in-window delete not caught: err=%v", err)
	}
}

// TestTrustedIndexReloadRejectsNewDamage: a NEW ghost live-bit (an entry row
// missing beyond the fossil baseline) must fail reconciliation instead of
// silently shipping a smaller index.
func TestTrustedIndexReloadRejectsNewDamage(t *testing.T) {
	store := newMapStore()
	_, _ = buildFlushedTree(t, store, 20)

	miner := New()
	miner.SetCold(ColdReaderFromGetter(store))
	if err := miner.LoadFrom(store); err != nil {
		t.Fatalf("full load: %v", err)
	}
	baseDelta := miner.LiveBits() - miner.LiveCount()
	prevCursor := miner.NextSlot()

	// Vandalize: delete a live slot's entry row behind the tree's back, then
	// force that twig through the rescan path by voiding the trust boundary
	// below it. Depending on where the victim lands, either reconciliation
	// (sealed twig: leaf blob hydrates, only the index entry is missing) or
	// the twig-meta consistency check (active twig: the leaf itself cannot be
	// rebuilt) must reject the reload — never a silent smaller index.
	var victim uint64
	mi := miner.idx.(*flatIndex)
	for i := range mi.ctrl {
		if mi.ctrl[i]&0x80 != 0 {
			victim = mi.slots[i]
			break
		}
	}
	if err := store.Delete(EntryTable, be8(victim)); err != nil {
		t.Fatal(err)
	}
	miner.SetIndex(NewMapIndex()) // force a full rescan (trust boundary 0)
	err := miner.LoadFromTrustedIndex(store, 0, baseDelta)
	if err == nil {
		t.Fatal("new damage (deleted live entry row) not caught by any reload check")
	}

	// The prevCursor path (fast, damage in the trusted zone) cannot SEE the
	// row loss — that is exactly what the write-time reproduce guard and the
	// fossil baseline bookkeeping are for. Documented, not asserted.
	_ = prevCursor
}

// TestLoadFromClearsDeadFlushed reproduces the ghost-live-bit root cause: an
// execution marks flushed slots dead in RAM, its tx ROLLS BACK (rows survive
// on disk), a recovery reload follows — and the stale deadFlushed list must
// NOT let the next flush delete the still-live rows.
func TestLoadFromClearsDeadFlushed(t *testing.T) {
	store := newMapStore()
	tr, flushed := buildFlushedTree(t, store, 5)

	// Pick a live flushed key and find its slot.
	fi := tr.idx.(*flatIndex)
	var key Hash
	var slot uint64
	for i := range fi.ctrl {
		if fi.ctrl[i]&0x80 != 0 && fi.slots[i] < flushed {
			key, slot = fi.keys[i], fi.slots[i]
			break
		}
	}

	// Failed execution: overwrite the key (old slot goes to deadFlushed), then
	// the enclosing tx "rolls back" — on disk nothing happened; recover by
	// reloading from the store.
	tr.Set(key, []byte("doomed-value"))
	tr.Root()
	if err := tr.LoadFrom(store); err != nil {
		t.Fatalf("recovery reload: %v", err)
	}

	// Next block flushes. With the stale deadFlushed list carried across the
	// reload, this deleted the LIVE slot's row (the ghost bit factory).
	rvApply(tr, stressOps(6))
	tr.Root()
	if _, _, err := tr.FlushTo(store, tr.EntriesBase()); err != nil {
		t.Fatalf("post-recovery flush: %v", err)
	}

	row, err := store.GetOne(EntryTable, be8(slot))
	if err != nil {
		t.Fatal(err)
	}
	if len(row) == 0 {
		t.Fatalf("live slot %d's entry row was deleted after a recovery reload (deadFlushed carryover)", slot)
	}
	if got, _ := tr.Get(key); string(got) == "doomed-value" {
		t.Fatal("rolled-back value visible after recovery reload")
	}
}
