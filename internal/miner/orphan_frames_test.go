// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
)

// TestReleaseLostFramesRefillsFirst: the leader sealed height H twice and
// dropped the second seal as a divergent sibling. The dropped block's
// frames that the kept block does not hold are fillable again at once, so
// the next fill takes them first in arrival order; the frames the kept
// block holds stay in flight.
func TestReleaseLostFramesRefillsFirst(t *testing.T) {
	st, hashes := frameStoreOf(t, 6, 2)
	bc := &internal.BlockChain{}
	bc.SetFrameStore(st)
	w := &worker{chain: bc}
	const H = 30
	mk := func(extra byte, idx ...int) block.IBlock {
		hdr := &block.Header{Number: uint256.NewInt(H), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1),
			GasLimit: 1 << 30, Extra: []byte{extra}}
		var hs []types.Hash
		for _, i := range idx {
			hs = append(hs, hashes[i])
		}
		return block.NewFrameBlockFromReceipt(hdr, hs, nil, nil, nil)
	}
	kept := mk(1, 2, 3)
	lost := mk(2, 0, 1, 2)
	bc.MarkFramesInFlight(kept.Body().(*block.Body).FrameHashes, H)
	bc.MarkFramesInFlight(lost.Body().(*block.Body).FrameHashes, H)

	if p := pickFrames(st, bc.FramesInFlight(), types.Address{}, 1<<62, 1024); len(p.hashes) != 2 || p.hashes[0] != hashes[4] {
		t.Fatalf("before release: %d frames", len(p.hashes))
	}
	w.releaseLostFrames(lost, kept)
	p := pickFrames(st, bc.FramesInFlight(), types.Address{}, 1<<62, 1024)
	want := []types.Hash{hashes[0], hashes[1], hashes[4], hashes[5]}
	if len(p.hashes) != len(want) {
		t.Fatalf("after release picked %d frames, want %d", len(p.hashes), len(want))
	}
	for i := range want {
		if p.hashes[i] != want[i] {
			t.Fatalf("position %d out of order", i)
		}
	}
	if bc.OrphanFramesReleased() != 2 {
		t.Fatalf("released %d", bc.OrphanFramesReleased())
	}
}
