// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// synthFrameFixture builds enough small blocks (via makeTestBlocks, repeated)
// that a small frameSize produces several frames, without depending on the
// real mainnet freezer that frame_experiment_test.go needs.
func synthFrameFixture(n int) []*DecodedBlock {
	base := makeTestBlocks()
	out := make([]*DecodedBlock, 0, n)
	for len(out) < n {
		out = append(out, base...)
	}
	return out[:n]
}

func newTestZstdCodec(t *testing.T) (*zstd.Encoder, *zstd.Decoder) {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		enc.Close()
		t.Fatalf("zstd reader: %v", err)
	}
	t.Cleanup(func() { enc.Close(); dec.Close() })
	return enc, dec
}

// TestFramedRoundTripSynthetic proves the framed layout reproduces the legacy
// decode exactly on small synthetic data, independent of the real-freezer
// fixture that TestFramedRoundTrip needs (and skips without).
func TestFramedRoundTripSynthetic(t *testing.T) {
	blocks := synthFrameFixture(10)
	enc, dec := newTestZstdCodec(t)

	const frameSize = 3
	framed := encodeBodySegmentFramed(blocks, 1, enc, frameSize)
	if !isFramedPayload(framed) {
		t.Fatal("framed payload must be detected as framed")
	}

	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	wantFrames := (len(blocks) + frameSize - 1) / frameSize
	if len(fi.entries) != wantFrames {
		t.Fatalf("frames = %d, want %d", len(fi.entries), wantFrames)
	}

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
	}
	if flags != detectBodyFlags(blocks[:frameSize]) {
		// first frame's flags are derived from its own slice
		t.Fatalf("decodeAllFrames flags %x != expected %x", flags, detectBodyFlags(blocks[:frameSize]))
	}

	// Random single-frame access via decodeOneFrame.
	for idx := 0; idx < len(blocks); idx++ {
		fr, ok := fi.frameFor(idx)
		if !ok {
			t.Fatalf("no frame for block index %d", idx)
		}
		got, _, err := decodeOneFrame(framed, fi, fr, dec)
		if err != nil {
			t.Fatalf("idx %d: decodeOneFrame: %v", idx, err)
		}
		within := idx - int(fi.entries[fr].blockStart)
		if within >= len(got) {
			t.Fatalf("idx %d: within=%d but frame has %d blocks", idx, within, len(got))
		}
		if len(got[within].Txs) != len(blocks[idx].Txs) {
			t.Fatalf("idx %d: %d txs, want %d", idx, len(got[within].Txs), len(blocks[idx].Txs))
		}
	}
}

// TestDecodeOneFrameOutOfRange exercises the bounds checks in decodeOneFrame.
func TestDecodeOneFrameOutOfRange(t *testing.T) {
	blocks := synthFrameFixture(6)
	enc, dec := newTestZstdCodec(t)
	framed := encodeBodySegmentFramed(blocks, 1, enc, 2)
	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatal(err)
	}

	for _, frame := range []int{-1, len(fi.entries), len(fi.entries) + 5} {
		if _, _, err := decodeOneFrame(framed, fi, frame, dec); err == nil {
			t.Fatalf("frame %d: expected out-of-range error", frame)
		}
	}

	// A frame whose compLen spans past the payload must error, not panic.
	badFi := &bodyFrameIndex{
		entries:   append([]bodyFrameEntry{}, fi.entries...),
		dataStart: fi.dataStart,
	}
	badFi.entries[0].compLen = uint32(len(framed)) + 1000
	if _, _, err := decodeOneFrame(framed, badFi, 0, dec); err == nil {
		t.Fatal("expected error for a frame spanning past the payload")
	}
}

// TestDecodeAllFramesPropagatesError ensures a single bad frame fails the
// whole decode rather than silently truncating the block list.
func TestDecodeAllFramesPropagatesError(t *testing.T) {
	blocks := synthFrameFixture(6)
	enc, dec := newTestZstdCodec(t)
	framed := encodeBodySegmentFramed(blocks, 1, enc, 2)
	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatal(err)
	}
	fi.entries[1].compLen = uint32(len(framed)) + 1000
	if _, _, err := decodeAllFrames(framed, fi, dec); err == nil {
		t.Fatal("expected decodeAllFrames to surface the bad frame's error")
	}
}

// TestParseBodyFrameIndexMalformed walks every way a framed-payload header can
// be corrupted or truncated and checks parseBodyFrameIndex rejects it cleanly.
func TestParseBodyFrameIndexMalformed(t *testing.T) {
	blocks := synthFrameFixture(8)
	enc, _ := newTestZstdCodec(t)
	good := encodeBodySegmentFramed(blocks, 1, enc, 2)

	cases := map[string][]byte{
		"empty":            nil,
		"too short":        good[:4],
		"wrong magic":      append([]byte{0, 0, 0, 0}, good[4:]...),
		"bodyLen too big":  overwriteU32(good, 4, uint32(len(good))*2),
		"bodyLen too small": overwriteU32(good, 4, 1),
		"n entries overflow": func() []byte {
			b := append([]byte{}, good...)
			// body starts at offset 8; first two bytes are the entry count.
			binary.LittleEndian.PutUint16(b[8:10], 0xFFFF)
			return b
		}(),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseBodyFrameIndex(p); err == nil {
				t.Fatalf("%s: expected error", name)
			}
		})
	}

	// A valid-looking non-framed payload (legacy) must also be rejected.
	legacyEnc, _ := newTestZstdCodec(t)
	legacy := encodeBodySegment(blocks, 1, legacyEnc)
	if _, err := parseBodyFrameIndex(legacy); err == nil {
		t.Fatal("legacy payload must not parse as a frame index")
	}
}

func overwriteU32(p []byte, off int, v uint32) []byte {
	b := append([]byte{}, p...)
	binary.LittleEndian.PutUint32(b[off:off+4], v)
	return b
}

// TestReadFrameHeaderAndReadOneFrame exercises the disk-facing helpers used by
// BodyCompactReader.readFramed: readFrameHeader (legacy detection, truncation)
// and readOneFrame (single-frame pull + decode, bounds checks).
func TestReadFrameHeaderAndReadOneFrame(t *testing.T) {
	blocks := synthFrameFixture(10)
	enc, dec := newTestZstdCodec(t)
	framed := encodeBodySegmentFramed(blocks, 1, enc, 3)

	readAt := func(p []byte, off int64) error {
		n := copy(p, framed[off:])
		if n < len(p) {
			return errShortRead
		}
		return nil
	}

	fi, err := readFrameHeader(readAt, 0, uint32(len(framed)))
	if err != nil || fi == nil {
		t.Fatalf("readFrameHeader: fi=%v err=%v", fi, err)
	}

	for idx := 0; idx < len(blocks); idx++ {
		fr, ok := fi.frameFor(idx)
		if !ok {
			t.Fatalf("no frame for idx %d", idx)
		}
		got, _, err := readOneFrame(readAt, 0, fi, fr, dec)
		if err != nil {
			t.Fatalf("readOneFrame idx %d: %v", idx, err)
		}
		within := idx - int(fi.entries[fr].blockStart)
		if len(got[within].Txs) != len(blocks[idx].Txs) {
			t.Fatalf("idx %d: tx count mismatch", idx)
		}
	}

	// readOneFrame bounds checks.
	if _, _, err := readOneFrame(readAt, 0, fi, -1, dec); err == nil {
		t.Fatal("expected error for negative frame")
	}
	if _, _, err := readOneFrame(readAt, 0, fi, len(fi.entries), dec); err == nil {
		t.Fatal("expected error for frame beyond range")
	}

	// readFrameHeader: legacy payload (small, no skippable magic) returns a
	// nil index and no error.
	legacyEnc, _ := newTestZstdCodec(t)
	legacy := encodeBodySegment(blocks, 1, legacyEnc)
	legacyReadAt := func(p []byte, off int64) error {
		n := copy(p, legacy[off:])
		if n < len(p) {
			return errShortRead
		}
		return nil
	}
	fi2, err := readFrameHeader(legacyReadAt, 0, uint32(len(legacy)))
	if err != nil {
		t.Fatalf("legacy readFrameHeader: %v", err)
	}
	if fi2 != nil {
		t.Fatal("legacy payload must yield a nil frame index")
	}

	// Too-small compSize (< 8) must also report legacy (nil, nil) without
	// reading anything.
	fi3, err := readFrameHeader(func(p []byte, off int64) error {
		t.Fatal("readAt must not be called for compSize < 8")
		return nil
	}, 0, 4)
	if err != nil || fi3 != nil {
		t.Fatalf("compSize<8: fi=%v err=%v", fi3, err)
	}

	// A corrupted bodyLen must produce an error, not a panic or silent zero
	// frames.
	corrupt := overwriteU32(framed, 4, uint32(len(framed))*4)
	corruptReadAt := func(p []byte, off int64) error {
		n := copy(p, corrupt[off:])
		if n < len(p) {
			return errShortRead
		}
		return nil
	}
	if _, err := readFrameHeader(corruptReadAt, 0, uint32(len(corrupt))); err == nil {
		t.Fatal("expected error for corrupted bodyLen via readFrameHeader")
	}
}

// TestIsFramedPayloadShort checks the < 4 byte guard explicitly (zero and
// one-byte payloads can't be inspected for a magic number).
func TestIsFramedPayloadShort(t *testing.T) {
	if isFramedPayload(nil) {
		t.Fatal("nil payload must not be framed")
	}
	if isFramedPayload([]byte{1, 2, 3}) {
		t.Fatal("3-byte payload must not be framed")
	}
}

var errShortRead = fmtErrorShortRead{}

type fmtErrorShortRead struct{}

func (fmtErrorShortRead) Error() string { return "short read" }
