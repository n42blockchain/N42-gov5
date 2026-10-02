// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"errors"
	"testing"
)

// TestEnsureTableCompressedReadOnly exercises the read-only branch of
// EnsureTableCompressed: creating fresh, then returning the cached table on
// a second call.
func TestEnsureTableCompressedReadOnly(t *testing.T) {
	dir := t.TempDir()

	// Pre-create the table (as a writer) so its cidx exists for the
	// subsequent read-only freezer to discover.
	tbl, err := NewFreezerTableCompressed(dir, TableReceipts, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Append(0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := NewReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	t1, err := f.EnsureTableCompressed(TableReceipts, "c")
	if err != nil {
		t.Fatalf("EnsureTableCompressed (readonly, already open): %v", err)
	}
	if t1.Items() != 1 {
		t.Fatalf("Items: got %d want 1", t1.Items())
	}

	// Second table not yet open: readonly path creates via
	// NewFreezerTableCompressedReadOnly. Since its cidx doesn't exist, this
	// should error, exercising the readonly-error branch.
	if _, err := f.EnsureTableCompressed(TableSenders, "c"); err == nil {
		t.Fatalf("expected error creating nonexistent table in read-only freezer")
	}
}

func TestAppendBatchBlobErrors(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "blob", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// count <= 0 is a silent no-op.
	if err := tbl.AppendBatchBlob(0, 0, []byte("x")); err != nil {
		t.Fatalf("count=0: %v", err)
	}

	// Out-of-order start.
	if err := tbl.AppendBatchBlob(5, 1, []byte("x")); err == nil {
		t.Fatalf("expected out-of-order error")
	}

	// Read-only table rejects.
	if err := tbl.Append(0, []byte("seed")); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := NewFreezerTableCompressedReadOnly(dir, "blob", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err := ro.AppendBatchBlob(1, 1, []byte("x")); err == nil {
		t.Fatalf("expected read-only error")
	}

	// Closed table rejects.
	closedTbl, err := NewFreezerTableCompressed(dir, "blob2", "c")
	if err != nil {
		t.Fatal(err)
	}
	closedTbl.Close()
	if err := closedTbl.AppendBatchBlob(0, 1, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestAppendBatchEmptyAndClosed(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "appendbatch", "c")
	if err != nil {
		t.Fatal(err)
	}

	// Empty slice is a no-op.
	if err := tbl.AppendBatch(0, nil); err != nil {
		t.Fatalf("AppendBatch empty: %v", err)
	}

	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.AppendBatch(0, [][]byte{[]byte("x")}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on closed table, got %v", err)
	}
}

func TestRetrieveClosedAndBounds(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "retrieve", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Append(0, []byte("a")); err != nil {
		t.Fatal(err)
	}

	// Out of bounds.
	if _, err := tbl.Retrieve(5); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}

	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.Retrieve(0); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestTruncateHeadClosedAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "trunc", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Append(0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := NewFreezerTableReadOnly(dir, "trunc", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.TruncateHead(0); err == nil {
		t.Fatalf("expected read-only error")
	}
	ro.Close()
	if err := ro.TruncateHead(0); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

// TestOpenFreezerExtendedTablesReadOnly covers openFreezer's extended-table
// discovery loop in read-only mode, including the canonical-min/frozen-height
// recovery path across core + extended tables.
func TestOpenFreezerExtendedTablesReadOnly(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := covMakeFreezeData(5)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	// Extended table ahead of canonical height: should NOT be truncated on
	// reopen (hard rule in openFreezer), only read back as-is.
	st, err := f.EnsureTable(TableSenders, "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 7; i++ {
		if err := st.Append(i, []byte("s")); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := NewReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.Frozen() != 5 {
		t.Fatalf("Frozen: got %d want 5 (canonical min)", ro.Frozen())
	}
	senders := ro.Table(TableSenders)
	if senders == nil || senders.Items() != 7 {
		t.Fatalf("expected senders table untouched at 7 items, got %v", senders)
	}
}

// TestOpenFreezerAheadCanonicalTruncated covers the RW path where a
// canonical table is ahead and gets truncated back down on open.
func TestOpenFreezerAheadCanonicalTruncated(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := covMakeFreezeData(5)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	// Manually push the headers table ahead of the others to simulate an
	// interrupted write.
	headers := f.Table(TableHeaders)
	if err := headers.Append(5, []byte("extra-header")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	f2, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if f2.Frozen() != 5 {
		t.Fatalf("Frozen: got %d want 5 after realignment", f2.Frozen())
	}
	if f2.Table(TableHeaders).Items() != 5 {
		t.Fatalf("headers not truncated back: got %d items", f2.Table(TableHeaders).Items())
	}
}
