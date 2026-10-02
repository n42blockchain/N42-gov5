// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TestOpenBodyCompactMissingIndex covers the "open index" error branch: no
// bodyc.cidx at all (e.g. an empty or nonexistent directory).
func TestOpenBodyCompactMissingIndex(t *testing.T) {
	if _, err := OpenBodyCompact(t.TempDir()); err == nil {
		t.Fatal("expected an error opening a directory with no bodyc.cidx")
	}
	if _, err := OpenBodyCompact(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error opening a nonexistent directory")
	}
}

// TestEncodeBodyBlockNilBlock covers EncodeBodyBlock's nil-block guard.
func TestEncodeBodyBlockNilBlock(t *testing.T) {
	if _, err := EncodeBodyBlock(nil, 1); err == nil {
		t.Fatal("expected an error encoding a nil block")
	}
}

// TestDecodeBodyBlockErrors covers DecodeBodyBlock's three failure modes:
// not valid zstd, valid zstd but not a body segment, and a segment with other
// than exactly one block.
func TestDecodeBodyBlockErrors(t *testing.T) {
	if _, err := DecodeBodyBlock([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("expected a decompress error for non-zstd input")
	}

	enc, _ := newTestZstdCodec(t)
	defer enc.Close()
	junk := enc.EncodeAll([]byte("not a body segment"), nil)
	if _, err := DecodeBodyBlock(junk); err == nil {
		t.Fatal("expected a decode error for a zstd blob that isn't a body segment")
	}

	// A segment encoding zero blocks (and one encoding more than one) must be
	// rejected by DecodeBodyBlock's exactly-one-block check.
	segEnc, _ := newTestZstdCodec(t)
	defer segEnc.Close()
	zeroBlocks := encodeBodySegment(nil, 1, segEnc)
	if _, err := DecodeBodyBlock(zeroBlocks); err == nil {
		t.Fatal("expected an error decoding a zero-block body wire")
	}

	twoBlocks := encodeBodySegment(makeTestBlocks()[:2], 1, segEnc)
	if _, err := DecodeBodyBlock(twoBlocks); err == nil {
		t.Fatal("expected an error decoding a two-block body wire")
	}
}

// TestFrameForMiss covers bodyFrameIndex.frameFor's not-found branch.
func TestFrameForMiss(t *testing.T) {
	blocks := synthFrameFixture(6)
	enc, _ := newTestZstdCodec(t)
	defer enc.Close()
	framed := encodeBodySegmentFramed(blocks, 1, enc, 2)
	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fi.frameFor(len(blocks) + 100); ok {
		t.Fatal("frameFor must report false for an index past every frame")
	}
}

// TestReleaseDrainsFrameFromCache drives TakeBody over every block of one
// frame to exercise release's frame-cache branch, including the "frame fully
// drained, evict it" path, and then confirms a second release on an
// already-released block hits the no-match fallthrough without panicking.
func TestReleaseDrainsFrameFromCache(t *testing.T) {
	const frameSize = 4
	dir := t.TempDir()
	writeOneFramedSegmentStore(t, dir, frameSize*3, frameSize)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if sa := r.frameAhead; sa != nil {
			<-sa.done
		}
		r.Close()
	}()

	// Drain every block of frame 0 via TakeBody.
	for i := 0; i < frameSize; i++ {
		if _, err := r.TakeBody(uint64(i)); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	// The frame must have been evicted once fully drained.
	for _, c := range r.frameCache {
		if c.seg == 0 && c.start == 0 {
			t.Fatal("frame 0 should have been evicted after every block was taken")
		}
	}

	// Releasing an already-handled block again must be a harmless no-op
	// (release's final fallthrough: no cachedSeg match, no frameCache match).
	r.release(0)
}

// TestDecodeSegmentFramedPath drives a sequential TakeBody across a
// multi-segment FRAMED store so the second segment is decoded via
// decodeSegment's read-ahead path (startAhead -> decodeSegment), which must
// take the "fi != nil" branch and loop over every frame — distinct from
// readFramed's single-frame-at-a-time random access path.
func TestDecodeSegmentFramedPath(t *testing.T) {
	enc, _ := newTestZstdCodec(t)
	defer enc.Close()
	to := types.Address{0x55}
	const perSeg = HeaderSegmentSize
	const total = perSeg + 50
	blocks := make([]*DecodedBlock, total)
	for i := range blocks {
		blocks[i] = &DecodedBlock{Txs: []*transaction.Transaction{
			transaction.NewTransaction(uint64(i), types.Address{0x66}, &to,
				uint256.NewInt(1), 21000, uint256.NewInt(1), nil),
		}}
	}

	dir := t.TempDir()
	writeFramedMultiSegmentStore(t, dir, blocks, 300)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if r.Segments() != 2 {
		t.Fatalf("segments = %d, want 2", r.Segments())
	}

	// Sequential TakeBody through segment 0 triggers read-ahead decode of
	// segment 1 via decodeSegment, which for a framed segment must iterate
	// every frame (the branch this test targets).
	for i := uint64(0); i < perSeg; i++ {
		if _, err := r.TakeBody(i); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	// Segment 1 should now be resident (read-ahead completed) or decode on
	// demand correctly either way.
	for i := uint64(perSeg); i < total; i++ {
		blk, err := r.TakeBody(i)
		if err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
		if blk.Txs[0].Nonce() != i {
			t.Fatalf("block %d: wrong content", i)
		}
	}
}

// writeFramedMultiSegmentStore writes len(blocks)/HeaderSegmentSize(+1)
// segments, each framed at frameSize blocks per frame, into one cdat file.
func writeFramedMultiSegmentStore(t *testing.T, dir string, blocks []*DecodedBlock, frameSize int) {
	t.Helper()
	enc, _ := newTestZstdCodec(t)
	defer enc.Close()

	var idx []byte
	var all []byte
	var offset uint64
	for start := 0; start < len(blocks); start += HeaderSegmentSize {
		end := start + HeaderSegmentSize
		if end > len(blocks) {
			end = len(blocks)
		}
		payload := encodeBodySegmentFramed(blocks[start:end], 1, enc, frameSize)
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(payload)))
		all = append(all, sizeBuf[:]...)
		all = append(all, payload...)
		idx = append(idx, encodeBodyIdx(bodyIdxEntry{fileNum: 0, offset: offset})...)
		offset += 4 + uint64(len(payload))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bodyc.0000.cdat"), all, 0o644); err != nil {
		t.Fatalf("write cdat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bodyc.cidx"), idx, 0o644); err != nil {
		t.Fatalf("write cidx: %v", err)
	}
}
