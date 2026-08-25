// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func makeFreezeData(count int) *FreezeData {
	data := &FreezeData{
		Headers:    make([][]byte, count),
		Bodies:     make([][]byte, count),
		Receipts:   make([][]byte, count),
		Hashes:     make([][]byte, count),
		Difficulty: make([][]byte, count),
	}
	for i := 0; i < count; i++ {
		data.Headers[i] = []byte(fmt.Sprintf("header-%d", i))
		data.Bodies[i] = []byte(fmt.Sprintf("body-%d", i))
		data.Receipts[i] = []byte(fmt.Sprintf("receipt-%d", i))
		data.Hashes[i] = []byte(fmt.Sprintf("hash-%032d", i))
		data.Difficulty[i] = []byte(fmt.Sprintf("td-%d", i))
	}
	return data
}

func TestFreezerTableBasic(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "test", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Append items.
	for i := uint64(0); i < 100; i++ {
		data := []byte(fmt.Sprintf("item-%d", i))
		if err := tbl.Append(i, data); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if tbl.Items() != 100 {
		t.Fatalf("items: got %d, want 100", tbl.Items())
	}

	// Retrieve items.
	for i := uint64(0); i < 100; i++ {
		data, err := tbl.Retrieve(i)
		if err != nil {
			t.Fatalf("retrieve %d: %v", i, err)
		}
		want := []byte(fmt.Sprintf("item-%d", i))
		if !bytes.Equal(data, want) {
			t.Fatalf("item %d: got %q, want %q", i, data, want)
		}
	}

	// Out of bounds.
	if _, err := tbl.Retrieve(100); err != ErrOutOfBounds {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}

	// Verify Geth-compatible file layout.
	if _, err := os.Stat(filepath.Join(dir, "test.cidx")); err != nil {
		t.Fatalf("expected test.cidx: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "test.0000.cdat")); err != nil {
		t.Fatalf("expected test.0000.cdat: %v", err)
	}
}

func TestFreezerTableBatch(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "batch", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := make([][]byte, 50)
	for i := range items {
		items[i] = []byte(fmt.Sprintf("batch-%d", i))
	}
	if err := tbl.AppendBatch(0, items); err != nil {
		t.Fatal(err)
	}

	if tbl.Items() != 50 {
		t.Fatalf("items: got %d, want 50", tbl.Items())
	}

	data, err := tbl.Retrieve(25)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("batch-25")) {
		t.Fatalf("got %q, want %q", data, "batch-25")
	}
}

func TestFreezerTableRetrieveSequentialBatch(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "sequential", "c")
	if err != nil {
		t.Fatal(err)
	}

	entries := make([][]byte, BatchSize)
	seed := uint64(0x9e3779b97f4a7c15)
	for i := range entries {
		entries[i] = make([]byte, 384*1024)
		for j := 0; j < len(entries[i])/2; j++ {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			entries[i][j] = byte(seed)
		}
		copy(entries[i][len(entries[i])/2:], entries[i][:len(entries[i])/2])
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	blob := EncodeBatch(entries, enc)
	enc.Close()
	if !isZstdFrame(blob) {
		t.Fatal("test batch was not zstd-compressed")
	}
	if len(blob) <= sequentialBatchThreshold {
		t.Fatalf("test batch is too small for streaming path: %d", len(blob))
	}
	if err := WriteBatch(tbl, entries, blob); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	tbl, err = NewFreezerTableCompressedReadOnly(dir, "sequential", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(BatchSize)

	// Starting mid-batch exercises resume: preceding entries are streamed
	// and discarded, then the decoder remains positioned for the next item.
	for i := 17; i < len(entries); i++ {
		got, err := tbl.RetrieveSequential(uint64(i))
		if err != nil {
			t.Fatalf("sequential item %d: %v", i, err)
		}
		if !bytes.Equal(got, entries[i]) {
			t.Fatalf("sequential item %d mismatch", i)
		}
	}
	if tbl.batchCacheData != nil {
		t.Fatal("sequential reads unexpectedly populated whole-batch cache")
	}

	// Random access uses its independent DecodeAll decoder and must remain
	// valid after a sequential stream has been consumed.
	got, err := tbl.Retrieve(3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, entries[3]) {
		t.Fatal("random read after sequential stream mismatch")
	}
}

func TestFreezerTableRetrieveSequentialSmallBatchUsesCache(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "smallseq", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	entries := make([][]byte, BatchSize)
	for i := range entries {
		entries[i] = bytes.Repeat([]byte(fmt.Sprintf("small-%02d/", i)), 64)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	blob := EncodeBatch(entries, enc)
	enc.Close()
	if len(blob) > sequentialBatchThreshold {
		t.Fatalf("test batch unexpectedly exceeds small-batch threshold: %d", len(blob))
	}
	if err := WriteBatch(tbl, entries, blob); err != nil {
		t.Fatal(err)
	}

	got, err := tbl.RetrieveSequential(0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, entries[0]) {
		t.Fatal("small sequential item mismatch")
	}
	if tbl.batchCacheData == nil {
		t.Fatal("small sequential batch did not retain DecodeAll cache")
	}
}

func TestFreezerTableRetrieveSequentialIgnoresOrphanTail(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "orphanseq", "c")
	if err != nil {
		t.Fatal(err)
	}
	entries := make([][]byte, BatchSize)
	for i := range entries {
		entries[i] = bytes.Repeat([]byte(fmt.Sprintf("live-%02d/", i)), 128)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	live := EncodeBatch(entries, enc)
	if err := WriteBatch(tbl, entries, live); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	// Model a restarted compaction that rewrote the index but left old valid
	// zstd frames after the last indexed batch in this cdat segment.
	orphanRaw := make([]byte, sequentialBatchThreshold+1024*1024)
	seed := uint64(0xd1b54a32d192ed03)
	for i := range orphanRaw {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		orphanRaw[i] = byte(seed)
	}
	orphan := enc.EncodeAll(orphanRaw, nil)
	enc.Close()
	if len(live)+len(orphan) <= sequentialBatchThreshold {
		t.Fatalf("test tail is too small to exercise trimming: %d", len(live)+len(orphan))
	}
	f, err := os.OpenFile(filepath.Join(dir, "orphanseq.0000.cdat"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(orphan); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	tbl, err = NewFreezerTableCompressedReadOnly(dir, "orphanseq", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(BatchSize)
	for i, want := range entries {
		got, err := tbl.RetrieveSequential(uint64(i))
		if err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("item %d mismatch", i)
		}
	}
	if tbl.batchCacheData == nil {
		t.Fatal("trimmed live frame did not use small-batch cache")
	}
}

// TestDeleteOrphanFilesLocked verifies that orphan .cdat files with
// arbitrary segment numbers are removed in a single pass — not the old
// hardcoded 11-file window which left segments beyond the window orphaned
// across kill+resume cycles.
func TestDeleteOrphanFilesLocked(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "orphan", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Lay out fake orphan .cdat files spanning a 30-segment range,
	// straddling the old hardcoded 10-file window. We also place an
	// "below cutoff" file at fileNum 4 that must NOT be deleted, and
	// a same-table-but-wrong-extension file that must be left alone.
	cutoff := uint16(7)
	orphanNums := []uint16{4, 7, 8, 9, 10, 12, 17, 19, 27, 29}
	for _, fnum := range orphanNums {
		name := filepath.Join(dir, fmt.Sprintf("orphan.%04d.cdat", fnum))
		if err := os.WriteFile(name, []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Different table — must NOT be touched.
	if err := os.WriteFile(filepath.Join(dir, "other.0007.cdat"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	// Same table, raw (.rdat) extension — must NOT be touched (this table is .cdat).
	if err := os.WriteFile(filepath.Join(dir, "orphan.0007.rdat"), []byte("raw"), 0644); err != nil {
		t.Fatal(err)
	}

	tbl.mu.Lock()
	if err := tbl.deleteOrphanFilesLocked(cutoff); err != nil {
		tbl.mu.Unlock()
		t.Fatal(err)
	}
	tbl.mu.Unlock()

	// Files with fnum < cutoff must survive; files with fnum >= cutoff must be gone.
	for _, fnum := range orphanNums {
		name := filepath.Join(dir, fmt.Sprintf("orphan.%04d.cdat", fnum))
		_, err := os.Stat(name)
		switch {
		case fnum < cutoff:
			if err != nil {
				t.Errorf("file %d below cutoff was deleted: %v", fnum, err)
			}
		default:
			if !os.IsNotExist(err) {
				t.Errorf("orphan %d was NOT deleted (Stat err = %v)", fnum, err)
			}
		}
	}
	// Sentinels must survive.
	for _, name := range []string{"other.0007.cdat", "orphan.0007.rdat"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("sentinel %s was wrongly deleted: %v", name, err)
		}
	}
}

func TestFreezerTableTruncate(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "trunc", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	for i := uint64(0); i < 20; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("t-%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	if err := tbl.TruncateHead(10); err != nil {
		t.Fatal(err)
	}
	if tbl.Items() != 10 {
		t.Fatalf("after truncate: got %d, want 10", tbl.Items())
	}

	data, err := tbl.Retrieve(5)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("t-5")) {
		t.Fatalf("got %q, want %q", data, "t-5")
	}

	if _, err := tbl.Retrieve(10); err != ErrOutOfBounds {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}

	if err := tbl.Append(10, []byte("new-10")); err != nil {
		t.Fatal(err)
	}
	data, err = tbl.Retrieve(10)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("new-10")) {
		t.Fatalf("got %q, want %q", data, "new-10")
	}
}

func TestFreezerTableReopen(t *testing.T) {
	dir := t.TempDir()

	tbl, err := NewFreezerTable(dir, "reopen", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 10; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("r-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	tbl.Close()

	tbl2, err := NewFreezerTable(dir, "reopen", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl2.Close()

	if tbl2.Items() != 10 {
		t.Fatalf("after reopen: got %d items, want 10", tbl2.Items())
	}

	data, err := tbl2.Retrieve(7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("r-7")) {
		t.Fatalf("got %q, want %q", data, "r-7")
	}
}

func TestFreezerTablePruned(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "prune", "c")
	if err != nil {
		t.Fatal(err)
	}

	// Write enough data to create file 0000.
	for i := uint64(0); i < 10; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("p-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	tbl.Close()

	// Delete the data file to simulate pruning.
	os.Remove(filepath.Join(dir, "prune.0000.cdat"))

	// Reopen — should succeed (index still exists).
	tbl2, err := NewFreezerTable(dir, "prune", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl2.Close()

	if tbl2.Items() != 10 {
		t.Fatalf("items after prune: got %d, want 10", tbl2.Items())
	}

	// Retrieve should return ErrPruned.
	_, err = tbl2.Retrieve(5)
	if err != ErrPruned {
		t.Fatalf("expected ErrPruned, got %v", err)
	}
}

func TestFreezerTableRidx(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "hashes", "r")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Write fixed-size 32-byte items.
	for i := uint64(0); i < 10; i++ {
		hash := make([]byte, 32)
		hash[31] = byte(i)
		if err := tbl.Append(i, hash); err != nil {
			t.Fatal(err)
		}
	}

	// Verify ridx/rdat file layout.
	if _, err := os.Stat(filepath.Join(dir, "hashes.ridx")); err != nil {
		t.Fatalf("expected hashes.ridx: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hashes.0000.rdat")); err != nil {
		t.Fatalf("expected hashes.0000.rdat: %v", err)
	}

	data, err := tbl.Retrieve(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 || data[31] != 5 {
		t.Fatalf("unexpected hash data: %x", data)
	}
}

func TestFreezerFull(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Frozen() != 0 {
		t.Fatalf("initial frozen: got %d, want 0", f.Frozen())
	}

	data := makeFreezeData(5)

	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if f.Frozen() != 5 {
		t.Fatalf("frozen: got %d, want 5", f.Frozen())
	}

	hdr, err := f.Ancient(TableHeaders, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hdr, []byte("header-3")) {
		t.Fatalf("got %q, want %q", hdr, "header-3")
	}

	if !f.HasAncient(4) {
		t.Fatal("expected HasAncient(4) = true")
	}
	if f.HasAncient(5) {
		t.Fatal("expected HasAncient(5) = false")
	}

	if err := f.TruncateHead(3); err != nil {
		t.Fatal(err)
	}
	if f.Frozen() != 3 {
		t.Fatalf("after truncate: frozen=%d, want 3", f.Frozen())
	}

	// Reopen and verify persistence.
	f.Close()

	f2, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	if f2.Frozen() != 3 {
		t.Fatalf("after reopen: frozen=%d, want 3", f2.Frozen())
	}
	body, err := f2.Ancient(TableBodies, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte("body-1")) {
		t.Fatalf("got %q, want %q", body, "body-1")
	}
}

func TestFreezerReopenTruncatesToShortestTable(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Freeze(0, makeFreezeData(5)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	bodyTable, err := NewFreezerTable(dir, TableBodies, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := bodyTable.TruncateHead(4); err != nil {
		t.Fatal(err)
	}
	if err := bodyTable.Close(); err != nil {
		t.Fatal(err)
	}

	f2, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	if got := f2.Frozen(); got != 4 {
		t.Fatalf("after reopen: frozen=%d, want 4", got)
	}
	if _, err := f2.Ancient(TableBodies, 4); err != ErrOutOfBounds {
		t.Fatalf("expected ErrOutOfBounds for truncated body table, got %v", err)
	}
	if _, err := f2.Ancient(TableHeaders, 4); err != ErrOutOfBounds {
		t.Fatalf("expected header table to be truncated to 4 items, got %v", err)
	}
}

func TestFreezerTableOverwrite(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "overwrite", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Write 10 items.
	for i := uint64(0); i < 10; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if tbl.Items() != 10 {
		t.Fatalf("items: got %d, want 10", tbl.Items())
	}

	// Overwrite item 5 with new data.
	if err := tbl.Append(5, []byte("NEW5")); err != nil {
		t.Fatalf("overwrite at 5: %v", err)
	}
	// Items should now be 6 (0-5, items 6-9 truncated).
	if tbl.Items() != 6 {
		t.Fatalf("after overwrite: items=%d, want 6", tbl.Items())
	}

	// Verify overwritten data.
	data, err := tbl.Retrieve(5)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "NEW5" {
		t.Fatalf("item 5: got %q, want %q", data, "NEW5")
	}

	// Old items still intact.
	data, err = tbl.Retrieve(3)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v3" {
		t.Fatalf("item 3: got %q, want %q", data, "v3")
	}

	// Can append after overwrite.
	if err := tbl.Append(6, []byte("v6-new")); err != nil {
		t.Fatal(err)
	}
	if tbl.Items() != 7 {
		t.Fatalf("after re-append: items=%d, want 7", tbl.Items())
	}

	// Overwrite at 0 (full rewrite).
	if err := tbl.Append(0, []byte("ZERO")); err != nil {
		t.Fatal(err)
	}
	if tbl.Items() != 1 {
		t.Fatalf("after overwrite-0: items=%d, want 1", tbl.Items())
	}
	data, err = tbl.Retrieve(0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ZERO" {
		t.Fatalf("item 0: got %q, want %q", data, "ZERO")
	}
}
