// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// ethTEncodeSenderBatch mirrors freezer.EncodeBatch's wire format:
// [4B len][data][4B len][data]... with zstd compression applied, matching
// what loadBatch expects to decompress.
func ethTEncodeSenderBatch(t *testing.T, entries [][]byte) []byte {
	t.Helper()
	var raw []byte
	for _, e := range entries {
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(e)))
		raw = append(raw, lenBuf[:]...)
		raw = append(raw, e...)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}

func ethTWriteSenderSegment(t *testing.T, dir string, batches [][][]byte) {
	t.Helper()
	w, err := cscompact.NewSegmentStoreWriter(dir, "senders")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, batch := range batches {
		compressed := ethTEncodeSenderBatch(t, batch)
		if _, err := w.WriteSegment(compressed, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenSenderStoreMissingDirReturnsNil(t *testing.T) {
	// No senders.cidx exists — OpenSegmentStore reports zero segments,
	// so OpenSenderStore returns (nil, nil) rather than erroring.
	r, err := OpenSenderStore(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Fatal("expected nil reader for a store with zero segments")
	}
}

func TestOpenSenderStoreReadsBatchAndCaches(t *testing.T) {
	dir := t.TempDir()
	addr0 := make([]byte, 20)
	addr0[0] = 0xAA
	addr1 := make([]byte, 20)
	addr1[0] = 0xBB
	batch0 := [][]byte{addr0, addr1, nil}
	ethTWriteSenderSegment(t, dir, [][][]byte{batch0})

	r, err := OpenSenderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil reader")
	}
	defer r.Close()

	if got := r.MaxBlock(); got != uint64(freezer.BatchSize) {
		t.Fatalf("MaxBlock = %d, want %d", got, freezer.BatchSize)
	}

	data, err := r.ReadBlock(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 20 || data[0] != 0xAA {
		t.Fatalf("ReadBlock(0) = %x, want addr0", data)
	}

	// Re-read same batch to exercise the cache-hit path (no reload).
	data1, err := r.ReadBlock(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(data1) != 20 || data1[0] != 0xBB {
		t.Fatalf("ReadBlock(1) = %x, want addr1", data1)
	}

	// Nil entry.
	data2, err := r.ReadBlock(2)
	if err != nil {
		t.Fatal(err)
	}
	if data2 != nil {
		t.Fatalf("ReadBlock(2) = %x, want nil", data2)
	}

	// Index beyond cached entries in this batch.
	data3, err := r.ReadBlock(3)
	if err != nil {
		t.Fatal(err)
	}
	if data3 != nil {
		t.Fatalf("ReadBlock(3) = %x, want nil (beyond batch entries)", data3)
	}
}

func TestOpenSenderStoreReadBlockBeyondSegmentCount(t *testing.T) {
	dir := t.TempDir()
	addr0 := make([]byte, 20)
	ethTWriteSenderSegment(t, dir, [][][]byte{{addr0}})

	r, err := OpenSenderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Block number in a batch beyond the single written segment.
	data, err := r.ReadBlock(uint64(freezer.BatchSize) * 5)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatalf("ReadBlock beyond segments = %x, want nil", data)
	}
}

func TestOpenSenderStoreLoadsUncompressedBatch(t *testing.T) {
	// loadBatch falls back to treating raw bytes as uncompressed when
	// zstd decompression fails.
	dir := t.TempDir()
	addr := make([]byte, 20)
	addr[5] = 0x42
	var raw []byte
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(addr)))
	raw = append(raw, lenBuf[:]...)
	raw = append(raw, addr...)

	w, err := cscompact.NewSegmentStoreWriter(dir, "senders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteSegment(raw, ""); err != nil {
		t.Fatal(err)
	}
	w.Close()

	r, err := OpenSenderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	data, err := r.ReadBlock(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 20 || data[5] != 0x42 {
		t.Fatalf("ReadBlock(0) = %x, want addr", data)
	}
}

func TestOpenSenderStoreTwoBatchesSwitchesCache(t *testing.T) {
	dir := t.TempDir()
	addrA := make([]byte, 20)
	addrA[0] = 1
	addrB := make([]byte, 20)
	addrB[0] = 2
	// Two full-size batches so block numbers land in different segments.
	batch0 := make([][]byte, freezer.BatchSize)
	for i := range batch0 {
		batch0[i] = addrA
	}
	batch1 := make([][]byte, freezer.BatchSize)
	for i := range batch1 {
		batch1[i] = addrB
	}
	ethTWriteSenderSegment(t, dir, [][][]byte{batch0, batch1})

	r, err := OpenSenderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	d0, err := r.ReadBlock(0)
	if err != nil {
		t.Fatal(err)
	}
	if d0[0] != 1 {
		t.Fatalf("batch0 entry = %x, want addrA", d0)
	}

	d1, err := r.ReadBlock(uint64(freezer.BatchSize))
	if err != nil {
		t.Fatal(err)
	}
	if d1[0] != 2 {
		t.Fatalf("batch1 entry = %x, want addrB", d1)
	}
}
