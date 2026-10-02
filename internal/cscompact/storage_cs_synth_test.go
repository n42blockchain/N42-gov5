// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Synthetic (no external DB) round-trip tests for storage_cs.go.

package cscompact

import (
	"math/rand"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
)

func synthStorageEntries(seed int64, blockCount int, maxDistinctAddrs int) ([]StorageCSEntry, []uint32) {
	r := rand.New(rand.NewSource(seed))
	entries := make([]StorageCSEntry, 0, blockCount)
	perBlock := make([]uint32, blockCount)

	var addrs []types.Address
	for i := 0; i < maxDistinctAddrs; i++ {
		var a types.Address
		r.Read(a[:])
		addrs = append(addrs, a)
	}

	for b := 0; b < blockCount; b++ {
		n := r.Intn(4)
		perBlock[b] = uint32(n)
		for i := 0; i < n; i++ {
			var slot types.Hash
			r.Read(slot[:])
			e := StorageCSEntry{
				Address:     addrs[r.Intn(len(addrs))],
				Incarnation: uint64(r.Intn(3) + 1),
				Slot:        slot,
			}
			if r.Intn(2) == 0 {
				e.OldValue = make([]byte, r.Intn(30)+1)
				r.Read(e.OldValue)
			}
			entries = append(entries, e)
		}
	}
	return entries, perBlock
}

func TestStorageCSSynthRoundtripDictMode(t *testing.T) {
	entries, perBlock := synthStorageEntries(10, 500, 5)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	compressed := encodeStorageCSSegment(entries, perBlock, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	got, gotPerBlock, err := DecodeStorageCSSegment(compressed, dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("entry count: got %d want %d", len(got), len(entries))
	}
	for i := range gotPerBlock {
		if gotPerBlock[i] != perBlock[i] {
			t.Fatalf("perBlock[%d] mismatch", i)
		}
	}
	for i := range got {
		if got[i].Address != entries[i].Address {
			t.Fatalf("entry %d address mismatch", i)
		}
		if got[i].Incarnation != entries[i].Incarnation {
			t.Fatalf("entry %d incarnation mismatch: got %d want %d", i, got[i].Incarnation, entries[i].Incarnation)
		}
		if got[i].Slot != entries[i].Slot {
			t.Fatalf("entry %d slot mismatch", i)
		}
		if len(got[i].OldValue) != len(entries[i].OldValue) {
			t.Fatalf("entry %d oldvalue len mismatch", i)
		}
		for j := range got[i].OldValue {
			if got[i].OldValue[j] != entries[i].OldValue[j] {
				t.Fatalf("entry %d byte %d mismatch", i, j)
			}
		}
	}
}

func TestStorageCSSynthRoundtripRawMode(t *testing.T) {
	entries, perBlock := synthStorageEntries(11, 400, 1000)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()
	compressed := encodeStorageCSSegment(entries, perBlock, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	got, _, err := DecodeStorageCSSegment(compressed, dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("entry count: got %d want %d", len(got), len(entries))
	}
	for i := range got {
		if got[i].Address != entries[i].Address {
			t.Fatalf("entry %d address mismatch", i)
		}
		if got[i].Slot != entries[i].Slot {
			t.Fatalf("entry %d slot mismatch", i)
		}
	}
}

func TestStorageCSSynthRoundtripEmpty(t *testing.T) {
	var entries []StorageCSEntry
	perBlock := make([]uint32, 6)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()
	compressed := encodeStorageCSSegment(entries, perBlock, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	got, gotPerBlock, err := DecodeStorageCSSegment(compressed, dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
	if len(gotPerBlock) != 6 {
		t.Fatalf("expected 6 blocks, got %d", len(gotPerBlock))
	}
}

func TestDecodeStorageCSSegmentErrors(t *testing.T) {
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()

	t.Run("bad zstd data", func(t *testing.T) {
		if _, _, err := DecodeStorageCSSegment([]byte{9, 9, 9}, dec); err == nil {
			t.Fatal("expected error for invalid zstd data")
		}
	})

	t.Run("too short after decompress", func(t *testing.T) {
		compressed := enc.EncodeAll([]byte{1, 2}, nil)
		if _, _, err := DecodeStorageCSSegment(compressed, dec); err == nil {
			t.Fatal("expected error for too-short payload")
		}
	})
}
