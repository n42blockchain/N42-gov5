// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression tests for the 2026-07-16 validator wedge: a failed block's
// mid-execution flush/evict pushed its appended entries into an MDBX tx that
// was then rolled back, so the revert's entry reads hit vanished cold rows —
// "live appended slot N unreadable during revert" — and, worse, the failure
// struck MID-mutation, leaving the live tree half-reverted. Two fixes under
// test: (1) v2 undo records carry AppendedKeys so the truncation pass needs no
// entry reads at all; (2) ApplyUndo resolves every fallible read BEFORE the
// first mutation, so even a legacy v1 record fails cleanly with the tree
// untouched.

package qmdb

import (
	"encoding/binary"
	"testing"
)

// wedgeScenario builds the incident shape: a base tree (flushed+evicted), one
// recorded block whose appends are then flushed+evicted, and finally the
// block's cold rows deleted — simulating the rolled-back tx taking the rows
// with it. Returns the tree, the undo record, and the pre-block root.
func wedgeScenario(t *testing.T) (*Tree, mapStore, *BlockUndo, Hash, uint64) {
	t.Helper()
	store := newMapStore()
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

	undo := applyBlockRecorded(tr, []rvOp{
		{key: rvKey(90001), val: []byte("wedge-a")},
		{key: rvKey(90002), val: []byte("wedge-b")},
		{key: rvKey(1), val: []byte("wedge-ow")}, // overwrite: deactivation + append
		{key: rvKey(90003), val: []byte("wedge-c")},
	})

	// The failed block's own flush+evict (mid-execution, inside the tx that
	// will roll back): appends leave the resident window.
	flushAndEvict(t, tr, store, flushed)

	// The rollback: the tx's writes vanish. Delete the block's appended entry
	// rows from the store; the pre-block rows persist (they were committed by
	// earlier flushes).
	for s := undo.PrevNextSlot; s < tr.NextSlot(); s++ {
		var k [8]byte
		binary.BigEndian.PutUint64(k[:], s)
		if err := store.Delete(EntryTable, k[:]); err != nil {
			t.Fatalf("simulating rollback: %v", err)
		}
	}
	return tr, store, undo, preRoot, preNext
}

// TestApplyUndoAppendedKeysSurviveRolledBackFlush: with a v2 record (appended
// keys captured at Set time), the revert succeeds even though every appended
// entry row is gone — the exact incident that wedged node1.
func TestApplyUndoAppendedKeysSurviveRolledBackFlush(t *testing.T) {
	tr, _, undo, preRoot, preNext := wedgeScenario(t)

	if len(undo.AppendedKeys) == 0 {
		t.Fatal("recording did not capture appended keys")
	}
	if err := tr.ApplyUndo(undo); err != nil {
		t.Fatalf("ApplyUndo with appended keys: %v", err)
	}
	if got := tr.Root(); got != preRoot {
		t.Fatalf("root after revert=%x want %x", got[:8], preRoot[:8])
	}
	if tr.NextSlot() != preNext {
		t.Fatalf("cursor after revert=%d want %d", tr.NextSlot(), preNext)
	}
	if _, ok := tr.Get(rvKey(90001)); ok {
		t.Fatal("appended key still readable after revert")
	}
}

// TestApplyUndoLegacyRecordFailsWithTreeUntouched: a v1 record (no appended
// keys) hits the vanished rows and must fail — but CLEANLY: root, cursor and
// index identical to the pre-revert state, so the caller's reload fallback
// starts from a consistent tree instead of a half-mutated one.
func TestApplyUndoLegacyRecordFailsWithTreeUntouched(t *testing.T) {
	tr, _, undo, _, _ := wedgeScenario(t)

	preRoot := tr.Root() // post-block root: the state the failed revert must preserve
	preNext := tr.NextSlot()
	// The appended keys' entry rows are already gone (the simulated rollback),
	// so Get can't see their values — capture the lookup RESULT as-is and
	// require it to be bit-identical after the failed revert.
	preVal, preOK := tr.Get(rvKey(90002))
	undo.AppendedKeys = nil // simulate a legacy v1 record

	if err := tr.ApplyUndo(undo); err == nil {
		t.Fatal("expected the legacy revert to fail on vanished rows")
	}
	if got := tr.Root(); got != preRoot {
		t.Fatalf("failed revert mutated the tree: root=%x want %x", got[:8], preRoot[:8])
	}
	if tr.NextSlot() != preNext {
		t.Fatalf("failed revert moved the cursor: %d want %d", tr.NextSlot(), preNext)
	}
	if v, ok := tr.Get(rvKey(90002)); ok != preOK || string(v) != string(preVal) {
		t.Fatalf("failed revert changed a lookup: (%q,%v) want (%q,%v)", v, ok, preVal, preOK)
	}
}

// TestBlockUndoMarshalV2RoundTrip: the appended-keys section survives
// marshal/unmarshal, and v1 payloads (no section) still decode.
func TestBlockUndoMarshalV2RoundTrip(t *testing.T) {
	u := &BlockUndo{
		PrevNextSlot: 12345,
		Entries: []UndoEntry{
			{Slot: 7, KeyHash: rvKey(7), Value: []byte("old-7")},
		},
		AppendedKeys: []Hash{rvKey(100), rvKey(101), rvKey(102)},
	}
	got, err := UnmarshalBlockUndo(u.Marshal())
	if err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if got.PrevNextSlot != u.PrevNextSlot || len(got.Entries) != 1 || len(got.AppendedKeys) != 3 {
		t.Fatalf("v2 round trip mismatch: %+v", got)
	}
	for i := range u.AppendedKeys {
		if got.AppendedKeys[i] != u.AppendedKeys[i] {
			t.Fatalf("appended key %d mismatch", i)
		}
	}

	// Hand-built v1 payload (legacy persisted records): version byte 0x01,
	// no appended-keys section.
	v1 := []byte{0x01}
	v1 = appendUvarintForTest(v1, 42) // prevNextSlot
	v1 = appendUvarintForTest(v1, 0)  // entry count
	legacy, err := UnmarshalBlockUndo(v1)
	if err != nil {
		t.Fatalf("unmarshal v1: %v", err)
	}
	if legacy.PrevNextSlot != 42 || legacy.AppendedKeys != nil {
		t.Fatalf("v1 decode mismatch: %+v", legacy)
	}
}

func appendUvarintForTest(b []byte, v uint64) []byte {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}
