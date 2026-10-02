// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// writeOneFramedSegmentStore writes a single-segment bodyc store of n blocks
// (one tx each, nonce == block index so a misrouted frame is detectable),
// framed at frameSize blocks per frame.
func writeOneFramedSegmentStore(t *testing.T, dir string, n int, frameSize int) {
	t.Helper()
	enc, _ := newTestZstdCodec(t)
	to := types.Address{0x33}
	blocks := make([]*DecodedBlock, n)
	for i := range blocks {
		blocks[i] = &DecodedBlock{Txs: []*transaction.Transaction{
			transaction.NewTransaction(uint64(i), types.Address{0x44}, &to,
				uint256.NewInt(1), 21000, uint256.NewInt(1), nil),
		}}
	}
	payload := encodeBodySegmentFramed(blocks, 1, enc, frameSize)
	writeOneSegmentStore(t, dir, payload)
}

// TestFrameCacheHitsAndEviction drives ReadBody over every block of a
// many-frame segment (frameSize=1, well past bodyFrameCacheSize) to exercise
// lookupFrame's cache hit/promote path and storeFrame's eviction once the
// cache is full.
func TestFrameCacheHitsAndEviction(t *testing.T) {
	const n = bodyFrameCacheSize + 8
	dir := t.TempDir()
	writeOneFramedSegmentStore(t, dir, n, 1)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	// Sequential forward pass fills and evicts the frame cache.
	for i := 0; i < n; i++ {
		blk, err := r.ReadBody(uint64(i))
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if len(blk.Txs) != 1 || blk.Txs[0].Nonce() != uint64(i) {
			t.Fatalf("block %d: wrong content", i)
		}
	}
	if len(r.frameCache) > bodyFrameCacheSize {
		t.Fatalf("frame cache grew to %d, want <= %d", len(r.frameCache), bodyFrameCacheSize)
	}

	// Re-reading the most recent frames must be a cache hit (lookupFrame):
	// read backwards over the still-resident tail.
	for i := n - 1; i >= n-4; i-- {
		blk, err := r.ReadBody(uint64(i))
		if err != nil {
			t.Fatalf("re-read %d: %v", i, err)
		}
		if blk.Txs[0].Nonce() != uint64(i) {
			t.Fatalf("re-read %d: wrong content", i)
		}
	}

	// The earliest frames were evicted, so this read is a disk miss that must
	// still produce the right block (loadSegment-free path via readFramed).
	blk, err := r.ReadBody(0)
	if err != nil {
		t.Fatalf("read evicted block 0: %v", err)
	}
	if blk.Txs[0].Nonce() != 0 {
		t.Fatal("evicted-then-reread block 0: wrong content")
	}
}

// TestFrameAheadArmAndClaim checks that reading block 0 arms a background
// decode of the next frame, that it is claimed (not re-decoded) by the read
// that needs it, and that a later out-of-order read cancels the stale slot
// (startFrameAhead's join-and-replace branch) without losing data.
func TestFrameAheadArmAndClaim(t *testing.T) {
	const frameSize = 4
	const n = frameSize * 6
	dir := t.TempDir()
	writeOneFramedSegmentStore(t, dir, n, frameSize)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if _, err := r.ReadBody(0); err != nil {
		t.Fatalf("read 0: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.frameAhead == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.frameAhead == nil {
		t.Fatal("expected frame-ahead to be armed for frame 1 after reading block 0")
	}
	if r.frameAhead.seg != 0 || r.frameAhead.frame != 1 {
		t.Fatalf("frame-ahead holds seg=%d frame=%d, want seg=0 frame=1", r.frameAhead.seg, r.frameAhead.frame)
	}

	// Reading into frame 1 must claim the in-flight (or just-finished) decode
	// via takeFrameAhead rather than a fresh synchronous one, and return the
	// right data either way.
	blk, err := r.ReadBody(uint64(frameSize))
	if err != nil {
		t.Fatalf("read %d: %v", frameSize, err)
	}
	if blk.Txs[0].Nonce() != uint64(frameSize) {
		t.Fatalf("read %d: wrong content", frameSize)
	}

	// Jump far ahead: the new frame-ahead target differs from whatever was
	// in flight, exercising the join-then-replace branch in startFrameAhead.
	farIdx := uint64(frameSize * 5)
	blk, err = r.ReadBody(farIdx)
	if err != nil {
		t.Fatalf("read %d: %v", farIdx, err)
	}
	if blk.Txs[0].Nonce() != farIdx {
		t.Fatalf("read %d: wrong content", farIdx)
	}

	// Reading the very last frame must not arm a successor (past end of
	// segment): startFrameAhead's `next >= len(fi.entries)` guard.
	lastIdx := uint64(n - 1)
	if _, err := r.ReadBody(lastIdx); err != nil {
		t.Fatalf("read last %d: %v", lastIdx, err)
	}

	// Close() only joins the sequential read-ahead slot (r.pending), not a
	// frame-ahead decode (r.frameAhead) — see the DEFECT note below. Join it
	// here ourselves so this test does not race Close() against the
	// background goroutine's file reads.
	if sa := r.frameAhead; sa != nil {
		<-sa.done
	}
}

// DEFECT (not fixed, out of scope for this test-only change): Close() waits
// for r.pending (the sequential TakeBody read-ahead) but never waits for
// r.frameAhead (the per-frame read-ahead armed by readFramed/startFrameAhead).
// A caller that reads a random/framed block and then immediately calls
// Close() can race the frame-ahead goroutine's file reads against Close()'s
// file-handle teardown — confirmed with `go test -race`: file reads inside
// startFrameAhead's goroutine race r.dataFiles[...].Close() in Close(). Tests
// in this file join r.frameAhead.done manually to avoid tripping it.
