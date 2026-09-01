// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestFramedRoundTrip proves the framed layout reproduces the legacy decode
// exactly: same block count, same per-block tx counts, same flags — on real
// mainnet data, one frame at a time and all frames together.
func TestFramedRoundTrip(t *testing.T) {
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	blocks := loadOneSegment(t, 25_000_000)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	legacy := encodeBodySegment(blocks, 1, enc)
	if isFramedPayload(legacy) {
		t.Fatal("legacy payload must NOT look framed")
	}
	framed := encodeBodySegmentFramed(blocks, 1, enc, bodyFrameSize)
	if !isFramedPayload(framed) {
		t.Fatal("framed payload must be detected as framed")
	}

	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	wantFrames := (len(blocks) + bodyFrameSize - 1) / bodyFrameSize
	if len(fi.entries) != wantFrames {
		t.Fatalf("frames = %d, want %d", len(fi.entries), wantFrames)
	}

	// All frames concatenated must equal the original block list.
	all, flags, err := decodeAllFrames(framed, fi, dec)
	if err != nil {
		t.Fatalf("decodeAllFrames: %v", err)
	}
	if len(all) != len(blocks) {
		t.Fatalf("decoded %d blocks, want %d", len(all), len(blocks))
	}
	for i := range blocks {
		if len(all[i].Txs) != len(blocks[i].Txs) {
			t.Fatalf("block %d: %d txs, want %d", i, len(all[i].Txs), len(blocks[i].Txs))
		}
		if len(all[i].Withdrawals) != len(blocks[i].Withdrawals) {
			t.Fatalf("block %d: %d withdrawals, want %d", i, len(all[i].Withdrawals), len(blocks[i].Withdrawals))
		}
	}
	legacyRaw, err := dec.DecodeAll(legacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if flags != bodySegmentFlags(legacyRaw) {
		t.Fatalf("framed flags %x != legacy flags %x", flags, bodySegmentFlags(legacyRaw))
	}

	// Random single-block access must land in the right frame and return the
	// right block — this is the whole point of the change.
	for _, idx := range []int{0, 1, bodyFrameSize - 1, bodyFrameSize, 4095, len(blocks) - 1} {
		fr, ok := fi.frameFor(idx)
		if !ok {
			t.Fatalf("no frame for block index %d", idx)
		}
		got, _, err := decodeOneFrame(framed, fi, fr, dec)
		if err != nil {
			t.Fatalf("idx %d: %v", idx, err)
		}
		within := idx - int(fi.entries[fr].blockStart)
		if within >= len(got) {
			t.Fatalf("idx %d: within=%d but frame has %d blocks", idx, within, len(got))
		}
		if len(got[within].Txs) != len(blocks[idx].Txs) {
			t.Fatalf("idx %d: %d txs, want %d", idx, len(got[within].Txs), len(blocks[idx].Txs))
		}
	}

	t.Logf("legacy=%d framed=%d (%+.2f%%), frames=%d",
		len(legacy), len(framed),
		float64(len(framed)-len(legacy))/float64(len(legacy))*100, len(fi.entries))
}

// TestFramedFallback checks that framing off / frame >= segment returns the
// legacy layout, so callers can disable it without a branch.
func TestFramedFallback(t *testing.T) {
	if _, err := os.Stat(filepath.Join(frameExpDir, "bodyc.cidx")); err != nil {
		t.Skip("bodyc freezer not present")
	}
	blocks := loadOneSegment(t, 25_000_000)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	for _, f := range []int{0, -1, len(blocks), len(blocks) + 1} {
		p := encodeBodySegmentFramed(blocks, 1, enc, f)
		if isFramedPayload(p) {
			t.Errorf("frameSize=%d produced a framed payload; want legacy", f)
		}
	}
}
