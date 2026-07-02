// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// TestRetrimPlainTable retrims a plain (uncompressed, per-item) table and
// verifies pruned-below-start semantics plus continued appends.
func TestRetrimPlainTable(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "plain", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := tbl.Append(uint64(i), []byte(fmt.Sprintf("item-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	newStart, dropped, err := RetrimIndexToItem(dir, "plain", "c", 50)
	if err != nil {
		t.Fatal(err)
	}
	if newStart != 50 || dropped != 50 {
		t.Fatalf("retrim: got start=%d dropped=%d, want 50/50", newStart, dropped)
	}

	tbl, err = NewFreezerTable(dir, "plain", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if got := tbl.StartItem(); got != 50 {
		t.Fatalf("StartItem: got %d, want 50", got)
	}
	if got := tbl.Items(); got != 100 {
		t.Fatalf("Items: got %d, want 100", got)
	}
	if _, err := tbl.Retrieve(49); !errors.Is(err, ErrPruned) {
		t.Fatalf("Retrieve(49): got %v, want ErrPruned", err)
	}
	if tbl.Has(49) {
		t.Fatal("Has(49) should be false")
	}
	for _, i := range []uint64{50, 73, 99} {
		data, err := tbl.Retrieve(i)
		if err != nil {
			t.Fatalf("Retrieve(%d): %v", i, err)
		}
		if want := fmt.Sprintf("item-%d", i); !bytes.Equal(data, []byte(want)) {
			t.Fatalf("Retrieve(%d): got %q, want %q", i, data, want)
		}
	}

	// Appends continue at the absolute head.
	if err := tbl.Append(100, []byte("item-100")); err != nil {
		t.Fatal(err)
	}
	if data, err := tbl.Retrieve(100); err != nil || !bytes.Equal(data, []byte("item-100")) {
		t.Fatalf("Retrieve(100): %q %v", data, err)
	}

	// Truncating into the retained range works; into the pruned range fails.
	if err := tbl.TruncateHead(60); err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 60 {
		t.Fatalf("Items after truncate: got %d, want 60", got)
	}
	if data, err := tbl.Retrieve(59); err != nil || !bytes.Equal(data, []byte("item-59")) {
		t.Fatalf("Retrieve(59): %q %v", data, err)
	}
	if err := tbl.TruncateHead(40); !errors.Is(err, ErrPruned) {
		t.Fatalf("TruncateHead(40): got %v, want ErrPruned", err)
	}

	// Truncate to exactly start empties the table; appends resume at start.
	if err := tbl.TruncateHead(50); err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 50 {
		t.Fatalf("Items after truncate-to-start: got %d, want 50", got)
	}
	if err := tbl.Append(50, []byte("item-50b")); err != nil {
		t.Fatal(err)
	}
	if data, err := tbl.Retrieve(50); err != nil || !bytes.Equal(data, []byte("item-50b")) {
		t.Fatalf("Retrieve(50) after re-append: %q %v", data, err)
	}
}

// TestRetrimBatchedTable verifies retrim snaps to a batch boundary on a
// compressed batch-blob table and reads decode correctly afterwards
// (including the batch auto-detect probe staying within the retained range).
func TestRetrimBatchedTable(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "batched", "c")
	if err != nil {
		t.Fatal(err)
	}
	tbl.ForceBatchSize(8)
	// Write 8 batch blobs of 8 items each via the real batch-blob path: one
	// blob per batch, 8 cidx entries sharing the blob's (fileNum, offset).
	for b := 0; b < 8; b++ {
		var blob []byte
		for i := 0; i < 8; i++ {
			payload := []byte(fmt.Sprintf("payload-%d-with-some-compressible-padding-aaaaaaaa", b*8+i))
			var lenPrefix [4]byte
			lenPrefix[0] = byte(len(payload))
			blob = append(blob, lenPrefix[:]...)
			blob = append(blob, payload...)
		}
		if err := tbl.AppendBatchBlob(uint64(b*8), 8, blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	// Ask to start mid-batch: 21 lies inside batch [16,24) → snapped to 16.
	newStart, dropped, err := RetrimIndexToItem(dir, "batched", "c", 21)
	if err != nil {
		t.Fatal(err)
	}
	if newStart != 16 || dropped != 16 {
		t.Fatalf("retrim: got start=%d dropped=%d, want 16/16", newStart, dropped)
	}

	tbl, err = NewFreezerTableCompressed(dir, "batched", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if got := tbl.StartItem(); got != 16 {
		t.Fatalf("StartItem: got %d, want 16", got)
	}
	if got := tbl.Items(); got != 64 {
		t.Fatalf("Items: got %d, want 64", got)
	}
	if _, err := tbl.Retrieve(15); !errors.Is(err, ErrPruned) {
		t.Fatalf("Retrieve(15): got %v, want ErrPruned", err)
	}
	for _, i := range []uint64{16, 17, 23, 24, 40, 63} {
		data, err := tbl.Retrieve(i)
		if err != nil {
			t.Fatalf("Retrieve(%d): %v", i, err)
		}
		if want := fmt.Sprintf("payload-%d-with-some-compressible-padding-aaaaaaaa", i); !bytes.Equal(data, []byte(want)) {
			t.Fatalf("Retrieve(%d): got %q, want %q", i, data, want)
		}
	}
}

// TestRetrimIdempotentAndBounds covers the no-op and error paths.
func TestRetrimIdempotentAndBounds(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "b", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := tbl.Append(uint64(i), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	tbl.Close()

	if _, _, err := RetrimIndexToItem(dir, "b", "c", 4); err != nil {
		t.Fatal(err)
	}
	// Retrim below the current start is a no-op reporting the real start.
	s, d, err := RetrimIndexToItem(dir, "b", "c", 2)
	if err != nil || s != 4 || d != 0 {
		t.Fatalf("re-retrim below start: s=%d d=%d err=%v, want 4/0/nil", s, d, err)
	}
	// Beyond the head is an error.
	if _, _, err := RetrimIndexToItem(dir, "b", "c", 10); err == nil {
		t.Fatal("retrim beyond head should fail")
	}
	// A second real retrim requires clearing the previous backup first.
	if _, _, err := RetrimIndexToItem(dir, "b", "c", 6); err == nil {
		t.Fatal("second retrim with existing backup should fail")
	}
}
