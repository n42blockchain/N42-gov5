// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/golang/snappy"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// ethTHeaderFreezer builds a small geth-format header-only freezer (with
// matching body/receipt/hash/difficulty tables, required by Freezer.Freeze)
// using a mix of header shapes so a single HeaderCompactStage.Run pass
// exercises every optional column: baseFee, withdrawals, blob gas, the
// beacon root, the requests hash, and (for the pre-merge half) difficulty +
// nonce + uncle hash.
func ethTHeaderFreezer(t *testing.T, dir string, n int, preMerge bool) *freezer.Freezer {
	t.Helper()
	fz, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}

	var headers, bodies, receipts, hashes, diffs [][]byte
	var parent types.Hash
	for i := 0; i < n; i++ {
		h := &block.Header{
			ParentHash:  parent,
			Coinbase:    types.HexToAddress("0x01"),
			Root:        emptyTrieRoot(),
			ReceiptHash: EthReceiptHash(nil),
			TxHash:      emptyTrieRoot(),
			Number:      uint256.NewInt(uint64(i)),
			GasLimit:    30_000_000,
			GasUsed:     uint64(i) * 21000,
			Time:        tsAnchor + uint64(i),
			BaseFee:     uint256.NewInt(1_000_000_000 + uint64(i)),
		}
		wh := types.HexToHash("0xcafe")
		br := types.HexToHash("0xbeef")
		rh := types.HexToHash("0xf00d")
		blobUsed := uint64(131072)
		excess := uint64(0)
		h.WithdrawalsHash = &wh
		h.ParentBeaconRoot = &br
		h.RequestsHash = &rh
		h.BlobGasUsed = &blobUsed
		h.ExcessBlobGas = &excess
		if preMerge {
			h.Difficulty = uint256.NewInt(uint64(1000 + i))
			h.Nonce = block.BlockNonce{0, 0, 0, 0, 0, 0, 0, byte(i)}
			h.UncleHash = types.HexToHash("0xdead")
		} else {
			h.Difficulty = uint256.NewInt(0)
		}
		parent = h.Hash()

		raw, err := rlpEncodeTestHeader(t, h)
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, raw)
		bodies = append(bodies, emptyGethBodyRLP(t))
		receipts = append(receipts, snappy.Encode(nil, mustRLP(t, []interface{}{})))
		hb := h.Hash()
		hashes = append(hashes, hb[:])
		diffs = append(diffs, []byte{0})
	}
	if err := fz.Freeze(0, &freezer.FreezeData{
		Headers:    headers,
		Bodies:     bodies,
		Receipts:   receipts,
		Hashes:     hashes,
		Difficulty: diffs,
	}); err != nil {
		t.Fatal(err)
	}
	return fz
}

func rlpEncodeTestHeader(t *testing.T, h *block.Header) ([]byte, error) {
	t.Helper()
	return encodeGethHeader(t, h), nil
}

func TestHeaderCompactStageRunAndReaderRoundTrip(t *testing.T) {
	inDir := t.TempDir()
	fz := ethTHeaderFreezer(t, inDir, 20, false)
	defer fz.Close()

	outDir := t.TempDir()
	stage := NewHeaderCompactStage(fz, outDir)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	r, err := OpenHeaderCompact(outDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got := r.Segments(); got != 1 {
		t.Fatalf("Segments() = %d, want 1", got)
	}
	if got := r.MaxBlock(); got != HeaderSegmentSize {
		t.Fatalf("MaxBlock() = %d, want %d", got, HeaderSegmentSize)
	}

	for i := uint64(0); i < 20; i++ {
		want, err := fz.Ancient(freezer.TableHeaders, i)
		if err != nil {
			t.Fatal(err)
		}
		wantHdr, err := DecodeGethHeader(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.ReadHeader(i)
		if err != nil {
			t.Fatalf("ReadHeader(%d): %v", i, err)
		}
		if got.Hash() != wantHdr.Hash() {
			t.Fatalf("block %d: hash mismatch got=%x want=%x", i, got.Hash(), wantHdr.Hash())
		}
		if got.GasUsed != wantHdr.GasUsed {
			t.Fatalf("block %d: GasUsed got=%d want=%d", i, got.GasUsed, wantHdr.GasUsed)
		}
	}

	// Out-of-range read.
	if _, err := r.ReadHeader(1_000_000); err == nil {
		t.Fatal("expected error reading beyond MaxBlock")
	}

	// Running again is a no-op (already up to date).
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHeaderCompactStagePreMergeColumns(t *testing.T) {
	inDir := t.TempDir()
	fz := ethTHeaderFreezer(t, inDir, 5, true)
	defer fz.Close()

	outDir := t.TempDir()
	stage := NewHeaderCompactStage(fz, outDir)
	stage.SetFrameSize(0) // legacy whole-segment layout
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	r, err := OpenHeaderCompact(outDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for i := uint64(0); i < 5; i++ {
		want, err := fz.Ancient(freezer.TableHeaders, i)
		if err != nil {
			t.Fatal(err)
		}
		wantHdr, err := DecodeGethHeader(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.ReadHeader(i)
		if err != nil {
			t.Fatalf("ReadHeader(%d): %v", i, err)
		}
		if got.Hash() != wantHdr.Hash() {
			t.Fatalf("block %d: hash mismatch", i)
		}
	}
}

// TestHeaderCompactStageResumesAfterPartialSegment grows the source chain
// across a segment boundary (HeaderSegmentSize) and re-runs the stage against
// the same output dir. The first run leaves segment 0 full and segment 1
// partial; the second run must detect segment 1 as partial, rewind just that
// one segment (anchoring the new write position from segment 0's valid idx
// entry) and rewrite it with the now-complete data.
//
// NOTE: a chain under one full segment (so the only existing segment
// collapses to zero on rewind) hits a separate defect — see the coverage
// report; not exercised here because it would require asserting on
// corrupted output.
func TestHeaderCompactStageResumesAfterPartialSegment(t *testing.T) {
	const firstRun = HeaderSegmentSize + 5
	const secondRun = HeaderSegmentSize + 15

	inDir := t.TempDir()
	fz := ethTHeaderFreezer(t, inDir, firstRun, false)
	defer fz.Close()

	outDir := t.TempDir()
	stage := NewHeaderCompactStage(fz, outDir)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	inDir2 := t.TempDir()
	fz2 := ethTHeaderFreezer(t, inDir2, secondRun, false)
	defer fz2.Close()
	stage2 := NewHeaderCompactStage(fz2, outDir)
	if err := stage2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	r, err := OpenHeaderCompact(outDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Segments(); got != 2 {
		t.Fatalf("Segments() = %d, want 2", got)
	}
	for _, i := range []uint64{0, 1, HeaderSegmentSize - 1, HeaderSegmentSize, secondRun - 1} {
		want, err := fz2.Ancient(freezer.TableHeaders, i)
		if err != nil {
			t.Fatal(err)
		}
		wantHdr, err := DecodeGethHeader(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.ReadHeader(i)
		if err != nil {
			t.Fatalf("ReadHeader(%d): %v", i, err)
		}
		if got.Hash() != wantHdr.Hash() {
			t.Fatalf("block %d: hash mismatch after resume", i)
		}
	}
}

func TestHeaderCompactIdxEntryRoundTrip(t *testing.T) {
	e := headerIdxEntry{fileNum: 7, offset: 0x1_0000_0002}
	buf := encodeHeaderIdx(e)
	got := decodeHeaderIdx(buf)
	if got != e {
		t.Fatalf("decodeHeaderIdx(encodeHeaderIdx(e)) = %+v, want %+v", got, e)
	}
}

func TestOpenHeaderCompactMissingDirErrors(t *testing.T) {
	if _, err := OpenHeaderCompact(t.TempDir()); err == nil {
		t.Fatal("expected error opening a header compact store with no cidx")
	}
}
