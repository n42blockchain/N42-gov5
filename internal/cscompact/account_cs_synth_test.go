// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Synthetic (no external DB) round-trip and reader tests for account_cs.go.

package cscompact

import (
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
)

func synthAccountEntries(seed int64, blockCount int, maxDistinctAddrs int) ([]AccountCSEntry, []uint32) {
	r := rand.New(rand.NewSource(seed))
	entries := make([]AccountCSEntry, 0, blockCount)
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
			e := AccountCSEntry{Address: addrs[r.Intn(len(addrs))]}
			if r.Intn(3) != 0 {
				e.OldValue = make([]byte, r.Intn(30)+1)
				r.Read(e.OldValue)
			}
			entries = append(entries, e)
		}
	}
	return entries, perBlock
}

// TestAccountCSSynthRoundtripDictMode exercises the dictionary-mode encode path
// (few distinct addresses reused across many entries).
func TestAccountCSSynthRoundtripDictMode(t *testing.T) {
	entries, perBlock := synthAccountEntries(1, 500, 5)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	got, gotPerBlock, err := DecodeAccountCSSegment(compressed, dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("entry count: got %d want %d", len(got), len(entries))
	}
	for i := range gotPerBlock {
		if gotPerBlock[i] != perBlock[i] {
			t.Fatalf("perBlock[%d]: got %d want %d", i, gotPerBlock[i], perBlock[i])
		}
	}
	for i := range got {
		if got[i].Address != entries[i].Address {
			t.Fatalf("entry %d address mismatch", i)
		}
		if len(got[i].OldValue) != len(entries[i].OldValue) {
			t.Fatalf("entry %d oldvalue len mismatch: got %d want %d", i, len(got[i].OldValue), len(entries[i].OldValue))
		}
		for j := range got[i].OldValue {
			if got[i].OldValue[j] != entries[i].OldValue[j] {
				t.Fatalf("entry %d byte %d mismatch", i, j)
			}
		}
	}
}

// TestAccountCSSynthRoundtripRawMode exercises the raw-address encode path
// (many distinct addresses, so the dictionary would cost more than raw).
func TestAccountCSSynthRoundtripRawMode(t *testing.T) {
	entries, perBlock := synthAccountEntries(2, 400, 1000)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	got, _, err := DecodeAccountCSSegment(compressed, dec)
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
	}
}

// TestAccountCSSynthRoundtripEmpty covers the zero-entry edge case.
func TestAccountCSSynthRoundtripEmpty(t *testing.T) {
	var entries []AccountCSEntry
	perBlock := make([]uint32, 10)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	got, gotPerBlock, err := DecodeAccountCSSegment(compressed, dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
	if len(gotPerBlock) != 10 {
		t.Fatalf("expected 10 blocks, got %d", len(gotPerBlock))
	}
}

func TestDecodeAccountCSSegmentErrors(t *testing.T) {
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	defer enc.Close()

	t.Run("bad zstd data", func(t *testing.T) {
		if _, _, err := DecodeAccountCSSegment([]byte{1, 2, 3}, dec); err == nil {
			t.Fatal("expected error for invalid zstd data")
		}
	})

	t.Run("too short after decompress", func(t *testing.T) {
		compressed := enc.EncodeAll([]byte{1, 2}, nil)
		if _, _, err := DecodeAccountCSSegment(compressed, dec); err == nil {
			t.Fatal("expected error for too-short payload")
		}
	})
}

// buildAccountCSFiles writes a minimal acctcs.cidx + acctcs.0000.cdat pair for
// a single segment in dir, mirroring AccountCSCompactor.Run's on-disk format,
// without requiring a real kv.RoDB.
func buildAccountCSFiles(t *testing.T, dir string, entries []AccountCSEntry, perBlock []uint32) {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)

	datPath := filepath.Join(dir, "acctcs.0000.cdat")
	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(compressed)))
	data := append(append([]byte{}, sizeBuf[:]...), compressed...)
	if err := os.WriteFile(datPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	idxPath := filepath.Join(dir, "acctcs.cidx")
	var idxEntry [8]byte
	binary.LittleEndian.PutUint16(idxEntry[0:2], 0)
	binary.LittleEndian.PutUint32(idxEntry[4:8], 0)
	if err := os.WriteFile(idxPath, idxEntry[:], 0644); err != nil {
		t.Fatal(err)
	}
}

// TestAccountCSReaderSynth exercises OpenAccountCS/ReadBlock/Close against
// hand-built segment files, without needing a real changeset DB.
func TestAccountCSReaderSynth(t *testing.T) {
	dir := t.TempDir()
	entries, perBlock := synthAccountEntries(3, 50, 20)
	buildAccountCSFiles(t, dir, entries, perBlock)

	reader, err := OpenAccountCS(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if reader.segments != 1 {
		t.Fatalf("expected 1 segment, got %d", reader.segments)
	}

	for b := 0; b < len(perBlock); b++ {
		got, err := reader.ReadBlock(uint64(b))
		if err != nil {
			t.Fatalf("ReadBlock(%d): %v", b, err)
		}
		if uint32(len(got)) != perBlock[b] {
			t.Fatalf("block %d: got %d entries, want %d", b, len(got), perBlock[b])
		}
	}

	// Warm cache path: re-read same segment.
	if _, err := reader.ReadBlock(0); err != nil {
		t.Fatalf("warm ReadBlock: %v", err)
	}

	// Segment out of range.
	if err := reader.loadSegment(5); err == nil {
		t.Fatal("expected error for out-of-range segment")
	}
}
