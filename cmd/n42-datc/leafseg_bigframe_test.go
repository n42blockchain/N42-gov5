// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// spillOneFrame writes rows without ever cutting a frame, the way merge
// re-spills a whole bucket: the file ends up as ONE zstd frame.
func spillOneFrame(t *testing.T, dir string, n int) [][2][]byte {
	t.Helper()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	var want [][2][]byte
	rng := uint64(99)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	for i := 0; i < n; i++ {
		k := make([]byte, 36)
		k[0] = byte(next() % 2)
		binary.BigEndian.PutUint64(k[1:], next()%5000)
		binary.BigEndian.PutUint32(k[32:], uint32(next()%3))
		v := make([]byte, 40+int(next()%60))
		for j := range v {
			v[j] = byte(next())
		}
		if err := w.add(leafTableA, k, v); err != nil {
			t.Fatal(err)
		}
		want = append(want, [2][]byte{k, v})
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	return want
}

func countSegRows(t *testing.T, dir string) int {
	t.Helper()
	set, ok, err := openLeafSegSet(dir, leafTableA, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open seg set: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	c := set.Cursor()
	n := 0
	var prev []byte
	for k, _, e := c.Seek([]byte{0}); k != nil && e == nil; k, _, e = c.Next() {
		if prev != nil && bytes.Compare(prev, k) > 0 {
			t.Fatalf("segment rows out of order")
		}
		prev = append(prev[:0], k...)
		n++
	}
	return n
}

// TestFinalizeFrameLargerThanMergeSpan covers the 2026-09-16 merge data loss:
// merge re-spills a bucket as a single multi-GB frame, and finalize used to
// drop any frame whose span exceeded its resync bound — losing every row in
// that bucket. The frame must now be decoded whatever its size.
func TestFinalizeFrameLargerThanMergeSpan(t *testing.T) {
	savedSpan, savedStream := finalizeMergeSpan, finalizeStreamMin
	defer func() { finalizeMergeSpan, finalizeStreamMin = savedSpan, savedStream }()

	dir := t.TempDir()
	want := spillOneFrame(t, dir, 6000)
	spillFile := filepath.Join(dir, leafSpillDir, segFileName(leafTableA, 0)+".zspill")
	st, err := os.Stat(spillFile)
	if err != nil {
		t.Fatal(err)
	}
	// Bounds well below the single frame's size: the old code called it corrupt.
	finalizeMergeSpan = st.Size() / 4
	finalizeStreamMin = st.Size() / 8
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, leafSpillDir)); err == nil {
		t.Fatal("spill retained: the single frame was treated as corrupt")
	}
	if got := countSegRows(t, dir); got != len(want) {
		t.Fatalf("segment has %d rows, want %d", got, len(want))
	}
}

// TestFinalizeStreamingMatchesBuffered finalizes the same spill with the
// streaming decoder and with the buffered one; the segments must be identical.
func TestFinalizeStreamingMatchesBuffered(t *testing.T) {
	savedSpan, savedStream, savedRun := finalizeMergeSpan, finalizeStreamMin, finalizeRunBytes
	defer func() {
		finalizeMergeSpan, finalizeStreamMin, finalizeRunBytes = savedSpan, savedStream, savedRun
	}()

	base := t.TempDir()
	src := filepath.Join(base, "src")
	spillRows(t, src, 5, 4000)
	appendKillTail(t, filepath.Join(src, leafSpillDir, segFileName(leafTableS, 1)+".zspill"))
	spillRows(t, src, 6, 1500)

	buffered := filepath.Join(base, "buffered")
	streamed := filepath.Join(base, "streamed")
	copyTree(t, src, buffered)
	copyTree(t, src, streamed)

	finalizeRunBytes = 1 << 40
	finalizeStreamMin = 1 << 40 // everything buffered
	if err := finalizeLeafSegments(buffered); err != nil {
		t.Fatal(err)
	}
	finalizeStreamMin = 1 << 10 // everything streamed
	if err := finalizeLeafSegments(streamed); err != nil {
		t.Fatal(err)
	}
	requireSameTrees(t, buffered, streamed, "streaming vs buffered")
}
