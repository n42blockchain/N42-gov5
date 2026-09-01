// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
)

// writeOneHeaderSegmentStore lays down a minimal headerc store (one segment)
// so a real HeaderCompactReader can be pointed at it.
func writeOneHeaderSegmentStore(t *testing.T, dir string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dat := make([]byte, 0, 4+len(payload))
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
	dat = append(dat, sz[:]...)
	dat = append(dat, payload...)
	if err := os.WriteFile(filepath.Join(dir, "headerc.0000.cdat"), dat, 0o644); err != nil {
		t.Fatal(err)
	}
	var entry [8]byte // fileNum 0, offset 0
	if err := os.WriteFile(filepath.Join(dir, "headerc.cidx"), entry[:], 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHeaderFramedReaderMatchesLegacy pins that a framed headerc segment reads
// back identically to the same headers in the legacy whole-segment layout —
// the hash AND the derived Number, which is the headerc-specific trap: Number
// is not stored, it is reconstructed from the segment start, so a frame has to
// be told its own offset or every frame after the first is numbered wrong.
func TestHeaderFramedReaderMatchesLegacy(t *testing.T) {
	const n = 2048
	src := make([]*block.Header, n)
	for i := range src {
		h := makeTestHeader(byte(i))
		// makeTestHeader's seed is a byte, so give every header in the segment
		// distinct content -- otherwise frames would be byte-identical and a
		// frame-addressing bug could read the wrong frame undetected.
		h.Time = 1_700_000_000 + uint64(i)
		h.GasUsed = uint64(i) * 137
		h.Extra = []byte{byte(i), byte(i >> 8), 0x42}
		h.Number = uint256.NewInt(uint64(i))
		src[i] = h
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	legacyPayload := encodeHeaderSegment(src, enc)
	framedPayload := encodeHeaderSegmentFramed(src, enc, headerFrameSize)

	if isFramedPayload(legacyPayload) {
		t.Fatal("legacy payload must not carry the framed magic")
	}
	if !isFramedPayload(framedPayload) {
		t.Fatal("framed payload must carry the skippable-frame magic")
	}

	legacyDir := filepath.Join(t.TempDir(), "legacy")
	framedDir := filepath.Join(t.TempDir(), "framed")
	writeOneHeaderSegmentStore(t, legacyDir, legacyPayload)
	writeOneHeaderSegmentStore(t, framedDir, framedPayload)

	lr, err := OpenHeaderCompact(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()
	fr, err := OpenHeaderCompact(framedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()

	// Scattered order so consecutive reads land in different frames — the
	// pattern a single-frame cache turns into a re-decode on every read.
	probes := []uint64{0, 300, 1500, 255, 256, 257, 2047, 100, 1024, 700}
	for _, b := range probes {
		want, err := lr.ReadHeader(b)
		if err != nil {
			t.Fatalf("legacy block %d: %v", b, err)
		}
		got, err := fr.ReadHeader(b)
		if err != nil {
			t.Fatalf("framed block %d: %v", b, err)
		}
		if got.Hash() != want.Hash() {
			t.Errorf("block %d: hash %x != %x", b, got.Hash(), want.Hash())
		}
		if got.Number == nil || got.Number.Uint64() != b {
			t.Errorf("block %d: Number = %v, want %d", b, got.Number, b)
		}
	}

	// Every block, in order, so nothing is right only at the probe points.
	for b := uint64(0); b < n; b++ {
		want, err := lr.ReadHeader(b)
		if err != nil {
			t.Fatalf("legacy block %d: %v", b, err)
		}
		got, err := fr.ReadHeader(b)
		if err != nil {
			t.Fatalf("framed block %d: %v", b, err)
		}
		if got.Hash() != want.Hash() {
			t.Fatalf("block %d: hash %x != %x", b, got.Hash(), want.Hash())
		}
	}
}
