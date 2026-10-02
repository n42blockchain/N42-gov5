// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestGetDataFileSizePruned exercises the ErrPruned branch of
// getDataFileSize when the backing .cdat file has been removed (simulating
// cold-offload pruning) and no ColdResolver is installed.
func TestGetDataFileSizePruned(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "prunetest", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 3; i++ {
		if err := tbl.Append(i, []byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	// Remove the data file to simulate it being cold-offloaded/pruned.
	datPath := filepath.Join(dir, "prunetest.0000.cdat")
	if err := os.Remove(datPath); err != nil {
		t.Fatal(err)
	}

	tbl, err = NewFreezerTable(dir, "prunetest", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if _, err := tbl.Retrieve(2); !errors.Is(err, ErrPruned) {
		t.Fatalf("expected ErrPruned retrieving last item after data file removal, got %v", err)
	}
}

// TestReadIndexCorruptedFile exercises readIndex's error path when the cidx
// file is truncated shorter than the requested entry's offset.
func TestReadIndexCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "corrupt", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 5; i++ {
		if err := tbl.Append(i, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	idxPath := filepath.Join(dir, "corrupt.cidx")
	info, err := os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to half its size, corrupting the tail entries.
	if err := os.Truncate(idxPath, info.Size()/2); err != nil {
		t.Fatal(err)
	}

	tbl, err = NewFreezerTable(dir, "corrupt", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// With a truncated index, Items() reflects fewer entries; try to read
	// past the truncated range directly via readIndex to hit the ReadAt
	// error branch (EOF).
	if _, err := tbl.readIndex(1000); err == nil {
		t.Fatalf("expected error reading index entry past truncated file")
	}
}

func TestFreezerTableCloseIdempotentAfterOps(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "closetest", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Append(0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Sync on a closed table does not check t.closed and surfaces the
	// underlying OS "file already closed" error rather than ErrClosed or nil
	// (see freezer_corrupt_cov_test.go note / final report: minor defect,
	// not fixed here per task scope). Assert it doesn't panic.
	if err := tbl.Sync(); err == nil {
		t.Fatalf("expected Sync on closed table to surface an error")
	}
	if err := tbl.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}
