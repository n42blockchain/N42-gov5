// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
)

// TestCompactStagesDefaultToFramed pins that the generation stages emit FRAMED
// segments without anyone passing a flag.
//
// This gap was live once already: encodeBodySegmentFramed and
// encodeHeaderSegmentFramed existed and were tested, but both Run loops still
// called the whole-segment encoders, so regenerating would have produced the
// same 8192-block segments under a new name and the "regenerated at F=256"
// claim would have been false with nothing failing.
//
// Framed is the DEFAULT rather than an opt-in flag for the same reason: a knob
// that must be remembered at every call site is a knob that gets forgotten.
func TestCompactStagesDefaultToFramed(t *testing.T) {
	b := NewBodyCompactStage(nil, "")
	if b.FrameSize() != bodyFrameSize {
		t.Errorf("BodyCompactStage default frame size = %d, want %d", b.FrameSize(), bodyFrameSize)
	}
	h := NewHeaderCompactStage(nil, "")
	if h.FrameSize() != headerFrameSize {
		t.Errorf("HeaderCompactStage default frame size = %d, want %d", h.FrameSize(), headerFrameSize)
	}

	// 0 must still be reachable: it is how a legacy-readable artefact is made.
	b.SetFrameSize(0)
	if b.FrameSize() != 0 {
		t.Errorf("SetFrameSize(0) did not take: %d", b.FrameSize())
	}
}

// TestFrameSizeZeroProducesLegacyLayout pins the escape hatch actually works —
// SetFrameSize(0) must produce bytes an older reader can open, not framed
// bytes with an empty index.
func TestFrameSizeZeroProducesLegacyLayout(t *testing.T) {
	hdrs := []*block.Header{makeTestHeader(1), makeTestHeader(2), makeTestHeader(3)}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	if isFramedPayload(encodeHeaderSegmentFramed(hdrs, enc, 0)) {
		t.Error("frameSize 0 must produce the legacy whole-segment layout")
	}
	if !isFramedPayload(encodeHeaderSegmentFramed(hdrs, enc, 1)) {
		t.Error("frameSize 1 over 3 headers must produce a framed payload")
	}
}
