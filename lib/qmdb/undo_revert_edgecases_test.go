package qmdb

import "testing"

// TestRecordDeactivationPoisonsOnUnreadableEntry forces recordDeactivation's
// defensive branch: a deactivation of a slot below entriesBase with no cold
// reader attached cannot read the entry back, so the record must be poisoned
// (broken=true, brokenSlot/brokenKey pinned) rather than silently producing a
// wrong historical root.
func TestRecordDeactivationPoisonsOnUnreadableEntry(t *testing.T) {
	tr := New()
	tr.SetLeafStore(LeafStoreFromGetter(newMapStore()))
	const n = uint64(2 * TwigSize)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	store := newMapStore()
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	tr.CommitFlush()
	// EvictThrough needs a non-nil cold reader to proceed at all, but point it
	// at an EMPTY store (not the one FlushTo actually wrote to): the evicted
	// slots become unreadable from cold even though eviction itself succeeds.
	tr.SetCold(ColdReaderFromGetter(newMapStore()))
	tr.EvictThrough(next)

	tr.StartUndoRecording()
	tr.Set(key(0), val(999)) // deactivates slot 0, which is now unreadable
	u := tr.StopUndoRecording()

	if !u.broken {
		t.Fatalf("expected the undo record to be poisoned")
	}
	if u.brokenSlot != 0 {
		t.Fatalf("brokenSlot = %d, want 0", u.brokenSlot)
	}
	if u.brokenKey != key(0) {
		t.Fatalf("brokenKey mismatch")
	}

	// Both consumers of a poisoned record must refuse it rather than produce a
	// wrong root.
	if err := tr.ApplyUndo(u); err == nil {
		t.Fatalf("ApplyUndo accepted a poisoned record")
	}
	if _, _, _, err := tr.ProofAt(key(1), []*BlockUndo{u}); err == nil {
		t.Fatalf("ProofAt accepted a poisoned record")
	}
}

// TestUnmarshalBlockUndoLegacyV1 covers the legacy (no appended-keys section)
// wire format, which a pre-v2 binary's persisted undo window would still hold.
func TestUnmarshalBlockUndoLegacyV1(t *testing.T) {
	u := &BlockUndo{
		PrevNextSlot: 5,
		Entries: []UndoEntry{
			{Slot: 2, KeyHash: key(2), Value: val(2)},
			{Slot: 4, KeyHash: key(4), Value: val(4)},
		},
	}
	full := u.Marshal() // always v2
	if full[0] != blockUndoVersionV2 {
		t.Fatalf("Marshal did not tag v2")
	}

	// Hand-build the v1 form: same body, but stop right after the entries
	// (no appendedCount / appended keys section), with version byte 0x01.
	v1 := append([]byte{blockUndoVersion}, full[1:]...)
	// Find where the entries section ends: re-encode manually by re-deriving
	// from a v2 decode and re-marshaling only the v1-shaped prefix.
	dv2, err := UnmarshalBlockUndo(full)
	if err != nil {
		t.Fatalf("UnmarshalBlockUndo(v2): %v", err)
	}
	// Reconstruct the v1 byte stream directly rather than truncating full[],
	// since full[] has the appended-keys section (empty here, but the count
	// uvarint byte is still present and must be excluded from a true v1 blob
	// for the "trailing bytes" check to pass).
	v1 = v1[:len(v1)-1] // drop the trailing appendedCount=0 uvarint (1 byte)

	dv1, err := UnmarshalBlockUndo(v1)
	if err != nil {
		t.Fatalf("UnmarshalBlockUndo(v1): %v", err)
	}
	if dv1.PrevNextSlot != dv2.PrevNextSlot || len(dv1.Entries) != len(dv2.Entries) {
		t.Fatalf("v1/v2 decode diverged: %+v vs %+v", dv1, dv2)
	}
	for i := range dv1.Entries {
		a, b := dv1.Entries[i], dv2.Entries[i]
		if a.Slot != b.Slot || a.KeyHash != b.KeyHash || string(a.Value) != string(b.Value) {
			t.Fatalf("entry %d diverged between v1 and v2 decode", i)
		}
	}
	if dv1.AppendedKeys != nil {
		t.Fatalf("v1 decode should leave AppendedKeys nil, got %v", dv1.AppendedKeys)
	}
}

// TestUnmarshalBlockUndoErrors drives the structural error branches: bad
// header, bad uvarints, truncated keyHash/value, truncated/overflowing
// appended-keys count, and trailing garbage.
func TestUnmarshalBlockUndoErrors(t *testing.T) {
	u := &BlockUndo{
		PrevNextSlot: 1,
		Entries:      []UndoEntry{{Slot: 0, KeyHash: key(0), Value: val(0)}},
		AppendedKeys: []Hash{key(0)},
	}
	full := u.Marshal()

	cases := map[string][]byte{
		"empty":            {},
		"too short":        {0x02},
		"bad version":      append([]byte{0x03}, full[1:]...),
		"truncated prevslot": {blockUndoVersionV2},
		"truncated keyhash": func() []byte {
			// version + prevNextSlot uvarint + count uvarint + slot uvarint, then
			// cut before the 32-byte keyHash is complete.
			b := append([]byte{}, full[:1+1+1+1+5]...)
			return b
		}(),
		"huge appended-key count": func() []byte {
			// Truncate right where appendedCount would be read, and substitute an
			// enormous uvarint that can't possibly fit in the remaining bytes.
			// Rebuild: re-decode entries only, then hand-craft the tail.
			dv2, _ := UnmarshalBlockUndo(full)
			noAK := dv2.Marshal()
			// noAK currently has a correct (small) appended count; locate and
			// replace its last few bytes with an oversized uvarint.
			trimmed := noAK[:len(noAK)-1-32] // drop appendedCount(1B) + 1 key(32B)
			trimmed = append(trimmed, 0xff, 0xff, 0xff, 0xff, 0x0f)
			return trimmed
		}(),
		"trailing garbage": append(append([]byte{}, full...), 0x00),
	}
	for name, blob := range cases {
		if _, err := UnmarshalBlockUndo(blob); err == nil {
			t.Fatalf("case %q: UnmarshalBlockUndo accepted a malformed blob", name)
		}
	}
}

// TestApplyUndoWithStorageNilRecord covers the nil-record guard.
func TestApplyUndoWithStorageNilRecord(t *testing.T) {
	tr := New()
	store := newMapStore()
	if _, err := tr.ApplyUndoWithStorage(store, nil, 0); err == nil {
		t.Fatalf("ApplyUndoWithStorage accepted a nil record")
	}
}

// TestApplyUndoWithStorageDropsTwigRows covers the "twig count shrank" branch:
// a block that pushed the tree past a twig boundary, when reverted, must
// delete that twig's now-stale meta and leaf-blob rows so a reload does not
// resurrect the truncated tail.
func TestApplyUndoWithStorageDropsTwigRows(t *testing.T) {
	tr := New()
	store := newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	// Fill exactly one twig, flush, so twig 0 exists and is persisted.
	for i := uint64(0); i < TwigSize; i++ {
		tr.Set(key(i), val(i))
	}
	flushed, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("FlushTo #1: %v", err)
	}
	tr.CommitFlush()
	preBlockRoot := tr.Root()
	preBlockTwigCount := len(tr.twigs)

	// Block: append enough to spill into twig 1, then flush again so the new
	// twig's rows exist on disk to be dropped by the revert.
	tr.StartUndoRecording()
	for i := TwigSize; i < TwigSize+10; i++ {
		tr.Set(key(uint64(i)), val(uint64(i)))
	}
	undo := tr.StopUndoRecording()
	if undo.broken {
		t.Fatalf("undo record unexpectedly poisoned")
	}
	flushed, _, err = tr.FlushTo(store, flushed)
	if err != nil {
		t.Fatalf("FlushTo #2: %v", err)
	}
	if len(tr.twigs) <= preBlockTwigCount {
		t.Fatalf("test setup did not actually grow the twig count")
	}
	if _, ok := store[TwigTable][string(be8(1))]; !ok {
		t.Fatalf("test setup: twig 1 meta row missing before revert")
	}

	newFlushed, err := tr.ApplyUndoWithStorage(store, undo, flushed)
	if err != nil {
		t.Fatalf("ApplyUndoWithStorage: %v", err)
	}
	if tr.Root() != preBlockRoot {
		t.Fatalf("root after revert = %x, want pre-block root %x", tr.Root(), preBlockRoot)
	}
	if len(tr.twigs) != preBlockTwigCount {
		t.Fatalf("twig count after revert = %d, want %d", len(tr.twigs), preBlockTwigCount)
	}
	if _, ok := store[TwigTable][string(be8(1))]; ok {
		t.Fatalf("dropped twig's meta row was not deleted from storage")
	}
	if _, ok := store[LeavesTable][string(be8(1))]; ok {
		t.Fatalf("dropped twig's leaf-blob row was not deleted from storage")
	}
	_ = newFlushed
}
