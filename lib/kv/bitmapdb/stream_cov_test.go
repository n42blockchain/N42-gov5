package bitmapdb

import (
	"testing"

	"github.com/RoaringBitmap/roaring/roaring64"
)

// TestG37BitmapStreamFullWalk exercises NewBitmapStream/HasNext/Next/ToBitmap/Close
// over a populated bitmap, verifying iteration order and that ToBitmap returns the
// same underlying bitmap, and that Close returns it to the pool without panicking.
func TestG37BitmapStreamFullWalk(t *testing.T) {
	bm := roaring64.New()
	want := []uint64{1, 2, 3, 100, 1 << 40}
	for _, v := range want {
		bm.Add(v)
	}

	s := NewBitmapStream(bm)

	got := make([]uint64, 0, len(want))
	for s.HasNext() {
		v, err := s.Next()
		if err != nil {
			t.Fatalf("Next returned error: %v", err)
		}
		got = append(got, v)
	}

	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d: got %d, want %d", i, got[i], want[i])
		}
	}

	out, err := s.ToBitmap()
	if err != nil {
		t.Fatalf("ToBitmap returned error: %v", err)
	}
	if out != bm {
		t.Fatalf("ToBitmap did not return the original bitmap")
	}

	s.Close()
}

// TestG37BitmapStreamEmpty exercises the empty-bitmap case: HasNext should
// immediately report false and ToBitmap should still return the (empty) bitmap.
func TestG37BitmapStreamEmpty(t *testing.T) {
	bm := roaring64.New()
	s := NewBitmapStream(bm)

	if s.HasNext() {
		t.Fatalf("HasNext on empty bitmap should be false")
	}

	out, err := s.ToBitmap()
	if err != nil {
		t.Fatalf("ToBitmap returned error: %v", err)
	}
	if out.GetCardinality() != 0 {
		t.Fatalf("expected empty bitmap, got cardinality %d", out.GetCardinality())
	}

	s.Close()
}
