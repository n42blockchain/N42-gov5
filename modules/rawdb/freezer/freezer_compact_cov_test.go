// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"fmt"
	"testing"
)

// covFillCompressedTable creates a compressed table at dir/name with n items.
func covFillCompressedTable(t *testing.T, dir, name string, n int) {
	t.Helper()
	tbl, err := NewFreezerTableCompressed(dir, name, "c")
	if err != nil {
		t.Fatalf("new table %s: %v", name, err)
	}
	for i := 0; i < n; i++ {
		if err := tbl.Append(uint64(i), []byte(fmt.Sprintf("%s-payload-%d", name, i))); err != nil {
			t.Fatalf("append %s/%d: %v", name, i, err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
}

func TestCompactTableRoundTrip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	covFillCompressedTable(t, src, TableReceipts, 200)

	if err := CompactTable(src, dst, TableReceipts, "c"); err != nil {
		t.Fatalf("CompactTable: %v", err)
	}

	srcTbl, err := NewFreezerTableCompressed(src, TableReceipts, "c")
	if err != nil {
		t.Fatal(err)
	}
	defer srcTbl.Close()
	dstTbl, err := NewFreezerTableCompressed(dst, TableReceipts, "c")
	if err != nil {
		t.Fatal(err)
	}
	defer dstTbl.Close()

	if dstTbl.Items() != srcTbl.Items() {
		t.Fatalf("items mismatch: src=%d dst=%d", srcTbl.Items(), dstTbl.Items())
	}
	for _, i := range []uint64{0, 1, 63, 64, 100, 199} {
		sd, err := srcTbl.Retrieve(i)
		if err != nil {
			t.Fatalf("src retrieve %d: %v", i, err)
		}
		dd, err := dstTbl.Retrieve(i)
		if err != nil {
			t.Fatalf("dst retrieve %d: %v", i, err)
		}
		if string(sd) != string(dd) {
			t.Fatalf("item %d mismatch: %q vs %q", i, sd, dd)
		}
	}
}

func TestCompactTableEmpty(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create an empty source table (0 items).
	tbl, err := NewFreezerTableCompressed(src, TableSenders, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	if err := CompactTable(src, dst, TableSenders, "c"); err != nil {
		t.Fatalf("CompactTable on empty table: %v", err)
	}
}

func TestCompactAllRoundTrip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	covFillCompressedTable(t, src, TableReceipts, 10)
	covFillCompressedTable(t, src, TableSenders, 10)
	covFillCompressedTable(t, src, TableAccountChanges, 10)
	covFillCompressedTable(t, src, TableStorageChanges, 10)
	covFillCompressedTable(t, src, TableLeavesJournal, 10)
	covFillCompressedTable(t, src, TableBlockWitness, 10)

	if err := CompactAll(src, dst); err != nil {
		t.Fatalf("CompactAll: %v", err)
	}

	for _, name := range []string{
		TableReceipts, TableSenders, TableAccountChanges,
		TableStorageChanges, TableLeavesJournal, TableBlockWitness,
	} {
		dstTbl, err := NewFreezerTableCompressed(dst, name, "c")
		if err != nil {
			t.Fatalf("open dst %s: %v", name, err)
		}
		if dstTbl.Items() != 10 {
			t.Fatalf("table %s: got %d items, want 10", name, dstTbl.Items())
		}
		dstTbl.Close()
	}
}

func TestCompactTableRotatesAcrossBatches(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Large enough to span multiple batches (BatchSize=64) and exercise the
	// progress-log branch (batchStart % 100000 == 0 at batchStart=0, always
	// hit) plus general multi-batch flow.
	covFillCompressedTable(t, src, TableReceipts, 1000)

	if err := CompactTable(src, dst, TableReceipts, "c"); err != nil {
		t.Fatalf("CompactTable: %v", err)
	}

	dstTbl, err := NewFreezerTableCompressed(dst, TableReceipts, "c")
	if err != nil {
		t.Fatal(err)
	}
	defer dstTbl.Close()
	if dstTbl.Items() != 1000 {
		t.Fatalf("items: got %d want 1000", dstTbl.Items())
	}
}
