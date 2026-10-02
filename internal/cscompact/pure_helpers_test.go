// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for small pure helper functions in analyze.go and history_analysis.go
// that need no external DB.

package cscompact

import (
	"testing"

	"github.com/RoaringBitmap/roaring/roaring64"
)

func TestTopN(t *testing.T) {
	m := map[[20]byte]uint64{}
	for i := 0; i < 30; i++ {
		var a [20]byte
		a[0] = byte(i)
		m[a] = uint64(i)
	}

	top := topN(m, 10)
	if len(top) != 10 {
		t.Fatalf("expected 10 entries, got %d", len(top))
	}
	// The top 10 by count should be values 20..29.
	seen := map[uint64]bool{}
	for _, af := range top {
		seen[af.Count] = true
	}
	for v := uint64(20); v < 30; v++ {
		if !seen[v] {
			t.Errorf("expected count %d among top entries", v)
		}
	}
}

func TestTopNFewerThanN(t *testing.T) {
	m := map[[20]byte]uint64{}
	var a, b [20]byte
	a[0], b[0] = 1, 2
	m[a] = 5
	m[b] = 7

	top := topN(m, 10)
	if len(top) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(top))
	}
}

func TestTopNEmpty(t *testing.T) {
	top := topN(map[[20]byte]uint64{}, 10)
	if len(top) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(top))
	}
}

func TestParseErigonBitmapValueEmpty(t *testing.T) {
	if got := ParseErigonBitmapValue(nil); got != 0 {
		t.Fatalf("expected 0 for nil input, got %d", got)
	}
	if got := ParseErigonBitmapValue([]byte{}); got != 0 {
		t.Fatalf("expected 0 for empty input, got %d", got)
	}
}

func TestParseErigonBitmapValueRoaring(t *testing.T) {
	bm := roaring64.New()
	bm.Add(1)
	bm.Add(5)
	bm.Add(100)
	data, err := bm.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	got := ParseErigonBitmapValue(data)
	if got != 3 {
		t.Fatalf("expected cardinality 3, got %d", got)
	}
}

// NOTE: a 4-byte-aligned, non-roaring-encoded buffer (the "sorted uint32
// list" case documented in ParseErigonBitmapValue) is deliberately not
// exercised here: roaring64.Bitmap.UnmarshalBinary/ReadFrom panics (rather
// than returning an error) on at least some malformed fixed-size inputs
// (observed with a 16-byte buffer of four big-endian uint32s), which crashes
// the process instead of letting ParseErigonBitmapValue fall through to its
// sorted-list / fallback branches. This looks like a pre-existing robustness
// bug in ParseErigonBitmapValue (or upstream roaring64); left unfixed per
// task scope (non-test code must not be modified).

func TestParseErigonBitmapValueFallback(t *testing.T) {
	// Unsorted values that aren't valid roaring data and aren't a multiple of
	// 4 bytes either way they're read; use a length not divisible by 4 and
	// not valid roaring, forcing the size-estimate fallback.
	buf := []byte{1, 2, 3, 4, 5, 6, 7}
	got := ParseErigonBitmapValue(buf)
	want := uint64(len(buf)) / 4
	if got != want {
		t.Fatalf("expected fallback estimate %d, got %d", want, got)
	}
}

func TestByteReaderReadAndReadByte(t *testing.T) {
	br := byteReader([]byte("hello"))

	b, err := br.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte: %v", err)
	}
	if b != 'h' {
		t.Fatalf("expected 'h', got %q", b)
	}

	buf := make([]byte, 5)
	n, err := br.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 5 || string(buf) != "hello" {
		t.Fatalf("Read mismatch: n=%d buf=%q", n, buf)
	}
}

func TestByteReaderReadByteEOF(t *testing.T) {
	var br byteReader
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("expected error on empty byteReader")
	}
}
