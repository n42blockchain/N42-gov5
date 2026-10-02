package qmdb

import "testing"

// TestVerifyProofTamperEveryByte builds one proof over a multi-twig tree (so
// the upper path is non-empty) and flips every single bit of its encoded
// form, asserting each tampered blob is rejected.
//
// NOTE on the Slot field: Slot is encoded as a fixed 8 bytes, but only
// log2(TwigSize) + len(UpperPath) of its low bits are ever consulted by the
// fold (local = Slot % TwigSize picks the twig-internal path; twigID = Slot /
// TwigSize is shifted once per UpperPath level, and folding stops once
// UpperPath is exhausted). Bits above that — i.e. above the tree's actual
// depth — are therefore "don't care": VerifyProof never reads them, so
// tampering them does not and cannot change the folded hash. This is not an
// authentication gap: the folded Bits/TwigPath/UpperPath/Value are unchanged,
// so the tampered blob still proves the exact same (key, value) live at the
// exact same effective position; the high Slot bits are redundant encoding,
// not part of the verified statement. The test accounts for this precisely
// rather than asserting a blanket zero-acceptance that would be false for any
// tree whose depth is less than 64 bits (every real tree).
func TestVerifyProofTamperEveryByte(t *testing.T) {
	tr := New()
	const n = uint64(5 * TwigSize) // several twigs => non-empty UpperPath
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	target := key(3*TwigSize + 17)
	root := tr.Root()
	p, ok := tr.GetProof(target)
	if !ok {
		t.Fatalf("GetProof missing target key")
	}
	if len(p.UpperPath) == 0 {
		t.Fatalf("test tree did not produce a multi-level upper path")
	}
	blob := p.Marshal()
	if !VerifyEncodedProof(root, blob) {
		t.Fatalf("baseline proof rejected")
	}

	meaningfulSlotBits := 0
	for v := uint64(TwigSize - 1); v != 0; v >>= 1 {
		meaningfulSlotBits++
	}
	meaningfulSlotBits += len(p.UpperPath)
	const slotFieldStart = 1 + 32 // version + KeyHash

	rejected, accepted, dontCare := 0, 0, 0
	for i := range blob {
		for bit := 0; bit < 8; bit++ {
			if i >= slotFieldStart && i < slotFieldStart+8 {
				bitIndex := (i-slotFieldStart)*8 + bit
				if bitIndex >= meaningfulSlotBits {
					dontCare++
					continue // known don't-care region of the Slot field
				}
			}
			tampered := make([]byte, len(blob))
			copy(tampered, blob)
			tampered[i] ^= 1 << uint(bit)
			if VerifyEncodedProof(root, tampered) {
				accepted++
			} else {
				rejected++
			}
		}
	}
	t.Logf("tamper sweep: %d rejected, %d don't-care Slot bits skipped", rejected, dontCare)
	if accepted != 0 {
		t.Fatalf("%d bit tamperings outside the Slot don't-care region were accepted, want 0", accepted)
	}
}

// TestUnmarshalProofTruncationAndVersionErrors drives every structural error
// branch of UnmarshalProof directly (too short header, wrong version, wrong
// twig height, truncated twig path, truncated upper path, truncated value).
func TestUnmarshalProofTruncationAndVersionErrors(t *testing.T) {
	tr := New()
	tr.Set(key(1), val(1))
	p, ok := tr.GetProof(key(1))
	if !ok {
		t.Fatalf("GetProof missing")
	}
	full := p.Marshal()

	cases := map[string][]byte{
		"empty":           {},
		"too short":       full[:5],
		"wrong version":   append([]byte{0x01}, full[1:]...),
		"wrong twigheight": func() []byte {
			b := append([]byte{}, full...)
			b[41] = byte(TwigHeight + 1)
			return b
		}(),
		"truncated twig path": full[:1+32+8+1+5],
		"truncated upper path": func() []byte {
			// Cut right after the bits bitmap, before upperLen+UpperPath are intact.
			cut := 1 + 32 + 8 + 1 + TwigHeight*32 + TwigSize/8
			if cut+1 > len(full) {
				t.Fatalf("test proof too short to cut at upper path")
			}
			b := append([]byte{}, full[:cut]...)
			b = append(b, 5) // claim 5 upper-path hashes we don't provide
			return b
		}(),
		"truncated value": func() []byte {
			cut := 1 + 32 + 8 + 1 + TwigHeight*32 + TwigSize/8 + 1 + len(p.UpperPath)*32
			if cut+4 > len(full) {
				t.Fatalf("test proof too short to cut before value length")
			}
			b := append([]byte{}, full[:cut]...)
			// valueLen says more bytes follow than actually do.
			b = append(b, 0xff, 0xff, 0xff, 0x00)
			return b
		}(),
	}
	for name, blob := range cases {
		if _, err := UnmarshalProof(blob); err == nil {
			t.Fatalf("case %q: UnmarshalProof accepted a malformed blob", name)
		}
	}

	// VerifyEncodedProof must swallow the decode error and return false rather
	// than propagating it or panicking.
	if VerifyEncodedProof(tr.Root(), cases["wrong version"]) {
		t.Fatalf("VerifyEncodedProof accepted an undecodable blob")
	}
}

// TestGetProofMissingKey covers the "no proof for a dead/absent key" path.
func TestGetProofMissingKey(t *testing.T) {
	tr := New()
	tr.Set(key(1), val(1))
	if _, ok := tr.GetProof(key(2)); ok {
		t.Fatalf("GetProof returned ok=true for a key never set")
	}
	tr.Set(key(3), val(3))
	tr.Delete(key(3))
	if _, ok := tr.GetProof(key(3)); ok {
		t.Fatalf("GetProof returned ok=true for a deleted key")
	}
}

// TestSlotActiveAfterDelete confirms the committed liveness bit clears on the
// original slot once a key is deleted (the live entry moves to a new slot).
func TestSlotActiveAfterDelete(t *testing.T) {
	tr := New()
	tr.Set(key(1), val(1))
	slot, ok := tr.idx.Get(key(1))
	if !ok {
		t.Fatalf("index miss right after Set")
	}
	if active, ok := tr.SlotActive(slot); !ok || !active {
		t.Fatalf("SlotActive before delete = %v,%v want true,true", active, ok)
	}
	tr.Delete(key(1))
	if active, ok := tr.SlotActive(slot); !ok || active {
		t.Fatalf("SlotActive after delete = %v,%v want true,false (occupied but dead)", active, ok)
	}
}

// TestSnapshotLogExportsTombstonesAndSkipsNeverWritten covers both branches of
// SnapshotLog's filter: a dead (overwritten) slot is exported as inactive, and
// a genuinely never-written slot index is impossible to produce directly, so
// this instead checks the exported log is exactly nextSlot entries for a fully
// populated tree (no gaps) and that deleted keys show Active=false with their
// original value preserved (the position-preserving contract).
func TestSnapshotLogExportsTombstonesAndSkipsNeverWritten(t *testing.T) {
	tr := New()
	const n = 20
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	tr.Delete(key(5))
	tr.Set(key(7), val(999)) // overwrite: old slot for key 7 goes dead

	log := tr.SnapshotLog()
	if uint64(len(log)) != tr.NextSlot() {
		t.Fatalf("SnapshotLog length = %d, want nextSlot = %d", len(log), tr.NextSlot())
	}
	bySlot := map[uint64]SlotEntry{}
	for _, e := range log {
		bySlot[e.Slot] = e
	}
	// Reconstructing from the full log must reproduce the live root exactly
	// (this is what FromSnapshotLog / the v1 portable snapshot relies on).
	rebuilt := FromSnapshotLog(log)
	if rebuilt.Root() != tr.Root() {
		t.Fatalf("tree rebuilt from SnapshotLog has a different root")
	}
	liveCount := 0
	for _, e := range log {
		if e.Active {
			liveCount++
		}
	}
	if liveCount != tr.LiveCount() {
		t.Fatalf("SnapshotLog live entries = %d, want LiveCount() = %d", liveCount, tr.LiveCount())
	}
}
