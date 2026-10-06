package qmdb

import (
	"bytes"
	"errors"
	"testing"

	"lukechampine.com/blake3"
)

func mkPortableSnapshot() *PortableSnapshot {
	tr := New()
	for i := uint64(0); i < 10; i++ {
		tr.Set(key(i), val(i))
	}
	tr.Delete(key(3))
	return &PortableSnapshot{
		ChainID:     1,
		GenesisHash: key(0x10),
		BlockNumber: 5,
		BlockHash:   key(0x20),
		Root:        tr.Root(),
		NextSlot:    tr.NextSlot(),
		Entries:     tr.SnapshotLog(),
	}
}

// TestValidatePortableSnapshotErrors drives every guard in
// validatePortableSnapshot directly: nil snapshot, entry-count mismatch,
// non-contiguous slot, and an oversized value.
func TestValidatePortableSnapshotErrors(t *testing.T) {
	if err := validatePortableSnapshot(nil); err == nil {
		t.Fatalf("validatePortableSnapshot(nil) accepted")
	}

	snap := mkPortableSnapshot()

	bad := *snap
	bad.NextSlot = snap.NextSlot + 1
	if err := validatePortableSnapshot(&bad); err == nil {
		t.Fatalf("validatePortableSnapshot accepted a NextSlot/entry-count mismatch")
	}

	bad2 := *snap
	bad2.Entries = append([]SlotEntry{}, snap.Entries...)
	bad2.Entries[2].Slot = 999
	if err := validatePortableSnapshot(&bad2); err == nil {
		t.Fatalf("validatePortableSnapshot accepted a non-contiguous slot log")
	}

	bad3 := *snap
	bad3.Entries = append([]SlotEntry{}, snap.Entries...)
	bad3.Entries[0].Value = make([]byte, maxPortableValueSize+1)
	if err := validatePortableSnapshot(&bad3); err == nil {
		t.Fatalf("validatePortableSnapshot accepted an oversized value")
	}

	if err := validatePortableSnapshot(snap); err != nil {
		t.Fatalf("validatePortableSnapshot rejected a well-formed snapshot: %v", err)
	}
}

// TestMarshalPortableSnapshotPropagatesValidationError confirms
// MarshalPortableSnapshot refuses an invalid snapshot before ever calling
// WritePortableSnapshot.
func TestMarshalPortableSnapshotPropagatesValidationError(t *testing.T) {
	if _, err := MarshalPortableSnapshot(nil); err == nil {
		t.Fatalf("MarshalPortableSnapshot(nil) accepted")
	}
}

// TestWritePortableSnapshotGuards covers the nil-source guard, a
// non-contiguous slot returned by the source, and an oversized value
// surfaced through the streaming writer (as opposed to the
// pre-materialized validatePortableSnapshot path).
func TestWritePortableSnapshotGuards(t *testing.T) {
	meta := PortableSnapshotMetadata{NextSlot: 2}
	if _, err := WritePortableSnapshot(&bytes.Buffer{}, meta, nil); err == nil {
		t.Fatalf("WritePortableSnapshot accepted a nil source")
	}

	if _, err := WritePortableSnapshot(&bytes.Buffer{}, meta, func(slot uint64) (SlotEntry, error) {
		return SlotEntry{Slot: slot + 1}, nil // wrong slot: breaks contiguity
	}); err == nil {
		t.Fatalf("WritePortableSnapshot accepted a non-contiguous source")
	}

	if _, err := WritePortableSnapshot(&bytes.Buffer{}, meta, func(slot uint64) (SlotEntry, error) {
		return SlotEntry{Slot: slot, Value: make([]byte, maxPortableValueSize+1)}, nil
	}); err == nil {
		t.Fatalf("WritePortableSnapshot accepted an oversized value")
	}

	wantErr := errors.New("boom")
	if _, err := WritePortableSnapshot(&bytes.Buffer{}, meta, func(slot uint64) (SlotEntry, error) {
		return SlotEntry{}, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("WritePortableSnapshot did not propagate the source error: %v", err)
	}
}

// TestUnmarshalPortableSnapshotErrors drives UnmarshalPortableSnapshot's
// structural checks: truncated input, corrupted digest, bad magic, entry
// count mismatch, overflowing entry count, non-contiguous slot, invalid
// active flag, oversized value length, and trailing bytes.
func TestUnmarshalPortableSnapshotErrors(t *testing.T) {
	snap := mkPortableSnapshot()
	good, err := MarshalPortableSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalPortableSnapshot: %v", err)
	}

	cases := map[string][]byte{
		"too short": good[:10],
		"bad digest": func() []byte {
			b := append([]byte{}, good...)
			b[len(b)-1] ^= 0xff
			return b
		}(),
		"bad magic": func() []byte {
			b := append([]byte{}, good...)
			b[0] ^= 0xff
			// Re-digest so the magic check (not the digest check) is what fails.
			payload := b[:len(b)-32]
			digest := blake3.Sum256(payload)
			return append(payload, digest[:]...)
		}(),
		"trailing bytes": func() []byte {
			payload := append([]byte{}, good[:len(good)-32]...)
			payload = append(payload, 0x00) // extra byte before the digest
			digest := blake3.Sum256(payload)
			return append(payload, digest[:]...)
		}(),
	}
	for name, blob := range cases {
		if _, err := UnmarshalPortableSnapshot(blob); err == nil {
			t.Fatalf("case %q: UnmarshalPortableSnapshot accepted a malformed blob", name)
		}
	}

	// Entry-section mutations: redigest after each so the digest check passes
	// and the structural check under test is what actually fails.
	redigest := func(payload []byte) []byte {
		d := blake3.Sum256(payload)
		return append(payload, d[:]...)
	}
	payloadOf := func() []byte { return append([]byte{}, good[:len(good)-32]...) }
	const (
		entryCountOff = 128 // second NextSlot/entryCount u64 in the header
		entry0Off     = portableSnapshotHeaderSize
	)

	// Entry count != NextSlot.
	bad := payloadOf()
	bad[entryCountOff] ^= 0x01
	if _, err := UnmarshalPortableSnapshot(redigest(bad)); err == nil {
		t.Fatalf("UnmarshalPortableSnapshot accepted entryCount != NextSlot")
	}

	// Entry count overflows remaining bytes: set it far beyond what's present.
	bad = payloadOf()
	for i := 0; i < 8; i++ {
		bad[entryCountOff+i] = 0xff
	}
	if _, err := UnmarshalPortableSnapshot(redigest(bad)); err == nil {
		t.Fatalf("UnmarshalPortableSnapshot accepted an overflowing entry count")
	}

	// Non-contiguous slot in the decoded entry stream (entry 0's slot field).
	bad = payloadOf()
	bad[entry0Off] = 7 // slot low byte: 0 -> 7, breaks "expected 0, got 7"
	if _, err := UnmarshalPortableSnapshot(redigest(bad)); err == nil {
		t.Fatalf("UnmarshalPortableSnapshot accepted a non-contiguous slot")
	}

	// Invalid active flag (must be 0 or 1).
	bad = payloadOf()
	bad[entry0Off+8] = 2
	if _, err := UnmarshalPortableSnapshot(redigest(bad)); err == nil {
		t.Fatalf("UnmarshalPortableSnapshot accepted an invalid active flag")
	}

	// Oversized declared value length (checked before the byte count, so no
	// backing bytes are needed to trigger it).
	bad = payloadOf()
	valLenOff := entry0Off + 8 + 1 + 32
	bad[valLenOff], bad[valLenOff+1], bad[valLenOff+2], bad[valLenOff+3] = 0xff, 0xff, 0xff, 0x7f
	if _, err := UnmarshalPortableSnapshot(redigest(bad)); err == nil {
		t.Fatalf("UnmarshalPortableSnapshot accepted an oversized value length")
	}

	// Round trip sanity on the unmodified blob.
	decoded, err := UnmarshalPortableSnapshot(good)
	if err != nil {
		t.Fatalf("UnmarshalPortableSnapshot(good): %v", err)
	}
	if decoded.Root != snap.Root || decoded.NextSlot != snap.NextSlot {
		t.Fatalf("decoded snapshot header diverged")
	}
}
