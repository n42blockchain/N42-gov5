package bodyf2

import (
	"errors"
	"testing"
)

func TestHashStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mk := func(a, b byte) [32]byte { var h [32]byte; h[0], h[1] = a, b; return h }

	w, err := NewHashWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	// seg 0: 2 blocks (2 txs, 1 tx); seg 2: 1 block (0 txs) — leave seg 1 a gap.
	if err := w.AppendSegment(0, [][][32]byte{{mk(1, 1), mk(1, 2)}, {mk(2, 1)}}); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendSegment(2, [][][32]byte{{}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenHashReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	hs, err := r.BlockHashes(0)
	if err != nil || len(hs) != 2 || hs[1] != mk(1, 2) {
		t.Fatalf("block 0 hashes: %v err=%v", hs, err)
	}
	hs1, err := r.BlockHashes(1)
	if err != nil || len(hs1) != 1 || hs1[0] != mk(2, 1) {
		t.Fatalf("block 1 hashes: %v err=%v", hs1, err)
	}
	empty, err := r.BlockHashes(2 * SegSize)
	if err != nil || len(empty) != 0 {
		t.Fatalf("seg2 block: want 0 hashes, got %v err=%v", empty, err)
	}
	if _, err := r.BlockHashes(1 * SegSize); !errors.Is(err, ErrHashAbsent) {
		t.Errorf("gap segment: want ErrHashAbsent, got %v", err)
	}
}
