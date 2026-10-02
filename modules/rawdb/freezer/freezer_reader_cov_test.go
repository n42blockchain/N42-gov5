// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// covMakeFreezeData builds a FreezeData of count synthetic entries.
func covMakeFreezeData(count int) *FreezeData {
	data := &FreezeData{
		Headers:    make([][]byte, count),
		Bodies:     make([][]byte, count),
		Receipts:   make([][]byte, count),
		Hashes:     make([][]byte, count),
		Difficulty: make([][]byte, count),
	}
	for i := 0; i < count; i++ {
		h := &block.Header{Number: uint256.NewInt(uint64(i))}
		raw, err := h.Marshal()
		if err != nil {
			panic(err)
		}
		data.Headers[i] = raw
		data.Bodies[i] = []byte("body")
		data.Receipts[i] = []byte("receipts")
		var hash types.Hash
		hash[0] = byte(i)
		data.Hashes[i] = hash[:]
		td := uint256.NewInt(uint64(i) + 1)
		data.Difficulty[i] = td.Bytes()
	}
	return data
}

func TestAncientReaderBasic(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data := covMakeFreezeData(3)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}

	r := NewAncientReader(f)

	if !r.HasAncient(0) || r.HasAncient(3) {
		t.Fatalf("HasAncient mismatch")
	}
	if r.Frozen() != 3 {
		t.Fatalf("Frozen: got %d want 3", r.Frozen())
	}

	hash, err := r.ReadCanonicalHash(1)
	if err != nil {
		t.Fatalf("ReadCanonicalHash: %v", err)
	}
	if hash[0] != 1 {
		t.Fatalf("hash mismatch: %x", hash)
	}

	// Bad hash length: write a header-table entry directly to hashes via
	// a fresh table with wrong length to hit the error path.
	if _, err := r.ReadCanonicalHash(100); err == nil {
		t.Fatalf("expected out-of-bounds error")
	}

	hdrRaw, err := r.ReadHeaderRaw(0)
	if err != nil || len(hdrRaw) == 0 {
		t.Fatalf("ReadHeaderRaw: %v", err)
	}
	hdr, err := r.ReadHeader(0)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Number.Uint64() != 0 {
		t.Fatalf("header number mismatch: %v", hdr.Number)
	}

	if _, err := r.ReadBodyRaw(1); err != nil {
		t.Fatalf("ReadBodyRaw: %v", err)
	}
	if _, err := r.ReadReceiptsRaw(1); err != nil {
		t.Fatalf("ReadReceiptsRaw: %v", err)
	}
	td, err := r.ReadTd(2)
	if err != nil {
		t.Fatalf("ReadTd: %v", err)
	}
	if td.Uint64() != 3 {
		t.Fatalf("td mismatch: %v", td)
	}

	// Senders/account/storage changes tables don't exist yet -> error.
	if _, err := r.ReadSendersRaw(0); err == nil {
		t.Fatalf("expected error for missing senders table")
	}
	if _, err := r.ReadAccountChangesRaw(0); err == nil {
		t.Fatalf("expected error for missing acctcs table")
	}
	if _, err := r.ReadStorageChangesRaw(0); err == nil {
		t.Fatalf("expected error for missing storcs table")
	}

	// Now create those tables via EnsureTable and populate, then read back.
	st, err := f.EnsureTable(TableSenders, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(0, []byte("sender0")); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadSendersRaw(0)
	if err != nil || !bytes.Equal(got, []byte("sender0")) {
		t.Fatalf("ReadSendersRaw after ensure: %v %q", err, got)
	}

	act, err := f.EnsureTableCompressed(TableAccountChanges, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := act.Append(0, []byte("acct0")); err != nil {
		t.Fatal(err)
	}
	got, err = r.ReadAccountChangesRaw(0)
	if err != nil || !bytes.Equal(got, []byte("acct0")) {
		t.Fatalf("ReadAccountChangesRaw after ensure: %v %q", err, got)
	}

	sto, err := f.EnsureTable(TableStorageChanges, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := sto.Append(0, []byte("sto0")); err != nil {
		t.Fatal(err)
	}
	got, err = r.ReadStorageChangesRaw(0)
	if err != nil || !bytes.Equal(got, []byte("sto0")) {
		t.Fatalf("ReadStorageChangesRaw after ensure: %v %q", err, got)
	}
}

func TestFreezerTableAndTableNames(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Table("nonexistent") != nil {
		t.Fatalf("expected nil for unknown table")
	}

	data := covMakeFreezeData(1)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if f.Table(TableHeaders) == nil {
		t.Fatalf("expected headers table to be open")
	}

	names := f.TableNames()
	found := false
	for _, n := range names {
		if n == TableHeaders {
			found = true
		}
	}
	if !found {
		t.Fatalf("TableNames missing headers: %v", names)
	}
}

func TestFreezerSetColdResolver(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.SetColdResolver("nonexistent", nil) {
		t.Fatalf("expected false for unknown table")
	}
	data := covMakeFreezeData(1)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if !f.SetColdResolver(TableHeaders, nil) {
		t.Fatalf("expected true for existing table")
	}
}

func TestFreezerEnsureTableIdempotent(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	t1, err := f.EnsureTable(TableSenders, "c")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := f.EnsureTable(TableSenders, "c")
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatalf("EnsureTable should return the same instance on repeated calls")
	}
}

func TestFreezerEnsureTableCompressedReopensExisting(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Headers table is opened non-compressed by New(); EnsureTableCompressed
	// should close and reopen it as compressed.
	t1, err := f.EnsureTableCompressed(TableHeaders, "c")
	if err != nil {
		t.Fatal(err)
	}
	if !t1.compressed {
		t.Fatalf("expected table to be compressed after EnsureTableCompressed")
	}

	// Calling again should be a no-op fast path (already compressed).
	t2, err := f.EnsureTableCompressed(TableHeaders, "c")
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatalf("expected same instance when already compressed")
	}
}

func TestFreezerNewReadOnly(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := covMakeFreezeData(2)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := NewReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if !ro.IsReadOnly() {
		t.Fatalf("expected read-only freezer")
	}
	if ro.Frozen() != 2 {
		t.Fatalf("Frozen: got %d want 2", ro.Frozen())
	}
	if err := ro.TruncateHead(1); err == nil {
		t.Fatalf("expected truncate error on read-only table")
	}
}

func TestCodesCoverageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadCodesCoverage(dir); ok {
		t.Fatalf("expected no coverage file initially")
	}
	if err := WriteCodesCoverage(dir, 12345); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadCodesCoverage(dir)
	if !ok || got != 12345 {
		t.Fatalf("ReadCodesCoverage: got (%d,%v) want (12345,true)", got, ok)
	}
}

func TestCodesCoverageTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	// Write a too-short file directly to hit the len(b) < 8 branch.
	path := filepath.Join(dir, CodesCoverageFile)
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCodesCoverage(dir); ok {
		t.Fatalf("expected short file to be rejected")
	}
}
