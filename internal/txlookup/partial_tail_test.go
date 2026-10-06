// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txlookup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestPartialTailRewind covers the store-level half of the weekly txindex
// rebuild: a run that ends mid-segment leaves a PARTIAL final segment, and the
// next run must rewrite it rather than resume after it. Without the rewind the
// resume arithmetic (whole segments from the base) skips straight past, and the
// blocks between the partial segment's end and its boundary are unindexed
// forever.
func TestPartialTailRewind(t *testing.T) {
	dir := t.TempDir()

	w, err := cscompact.NewSegmentStoreWriter(dir, "txindex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteSegment(buildEmptyDatV2(SegmentSize), ""); err != nil {
		t.Fatal(err)
	}
	fullOnlySize := cdatSize(t, dir)
	if _, err := w.WriteSegment(buildEmptyDatV2(12_345), ""); err != nil {
		t.Fatal(err)
	}
	w.Close()

	n, err := partialTailSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("partialTailSegments = %d, want 1", n)
	}

	w2, err := cscompact.NewSegmentStoreWriter(dir, "txindex")
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.TruncateLastSegment(); err != nil {
		t.Fatal(err)
	}
	if got := w2.SegmentCount(); got != 1 {
		t.Fatalf("SegmentCount after truncate = %d, want 1", got)
	}
	// The cdat must shrink too, or a weekly rebuild of the tail segment
	// accumulates orphaned frames inside the published artefact.
	if got := cdatSize(t, dir); got != fullOnlySize {
		t.Fatalf("cdat size after truncate = %d, want %d", got, fullOnlySize)
	}
	// The next write must land where the discarded one did.
	if _, err := w2.WriteSegment(buildEmptyDatV2(SegmentSize), ""); err != nil {
		t.Fatal(err)
	}
	w2.Close()

	if n, err = partialTailSegments(dir); err != nil || n != 0 {
		t.Fatalf("partialTailSegments after refill = %d (err %v), want 0", n, err)
	}
	st, err := cscompact.OpenSegmentStore(dir, "txindex")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := st.SegmentCount(); got != 2 {
		t.Fatalf("SegmentCount after refill = %d, want 2", got)
	}
	for i := uint64(0); i < 2; i++ {
		data, err := st.ReadSegmentData(i)
		if err != nil {
			t.Fatalf("read segment %d: %v", i, err)
		}
		if got := uint64(le32(data[4:8])); got != SegmentSize {
			t.Fatalf("segment %d blockCount = %d, want %d", i, got, SegmentSize)
		}
	}
}

// TestBuildRangeRewritesPartialTail is the end-to-end shape of the weekly job:
// build to a mid-segment height, then build again to a later mid-segment
// height. Before the rewind the second call was a no-op (resume jumped to the
// next SegmentSize boundary, past the requested end) and the segment stayed at
// its first-run block count.
func TestBuildRangeRewritesPartialTail(t *testing.T) {
	ancientPath := ancientForTest()
	if ancientPath == "" {
		t.Skip("Geth ancient not found")
	}
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dir := t.TempDir()
	b := NewSegmentBuilder(f, dir, testBodyTxHashes)
	if err := b.BuildRange(context.Background(), 0, 56_000); err != nil {
		t.Fatal(err)
	}
	if got := tailBlockCount(t, dir); got != 56_000 {
		t.Fatalf("first build tail blockCount = %d, want 56000", got)
	}

	if err := b.BuildRange(context.Background(), 0, 120_000); err != nil {
		t.Fatal(err)
	}
	if got := tailBlockCount(t, dir); got != 120_000 {
		t.Fatalf("second build tail blockCount = %d, want 120000 (partial tail was not rewritten)", got)
	}
}

func ancientForTest() string {
	for _, p := range []string{`e:\geth\geth\chaindata\ancient\chain`, `d:\geth\geth\chaindata\ancient\chain`} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func cdatSize(t *testing.T, dir string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, "txindex.0000.cdat"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func tailBlockCount(t *testing.T, dir string) uint64 {
	t.Helper()
	st, err := cscompact.OpenSegmentStore(dir, "txindex")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n := st.SegmentCount()
	if n == 0 {
		t.Fatal("no segments")
	}
	data, err := st.ReadSegmentData(n - 1)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(le32(data[4:8]))
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
