// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node stored"):
// sealedOnParent is handleSealed's height-level single-candidate guard --
// "keep only the first block sealed on a given parent" -- and it is only as
// good as how early recordSealedOnParent runs. It used to run in
// writeAndFinish, "after a successful import"; a second, independently-built
// seal on the SAME parent (a parked speculative task finishing while the
// first block's write was still in flight -- wider with
// N42_LEADER_WRITE_ASYNC=1, but not exclusive to it) could reach handleSealed
// while the map was still empty, be treated as the first/only candidate, and
// get pushed and proposed instead of being suppressed. The fix moves the
// record to handleSealed itself, before push/propose. This test pins the
// invariant the fix depends on: once a parent has a recorded candidate, a
// later, divergent seal on the same parent must never replace it.
func TestRecordSealedOnParentFirstSealWins(t *testing.T) {
	w := &worker{sealedOnParent: make(map[types.Hash]block.IBlock)}
	parent := types.BytesToHash([]byte{0xAA})

	h1 := testMiningHeader(100)
	h1.ParentHash = parent
	h1.Extra = append([]byte(nil), h1.Extra...)
	h1.Extra[0] = 0x01
	blk1 := block.NewBlock(h1, nil)

	h2 := testMiningHeader(100)
	h2.ParentHash = parent
	h2.Extra = append([]byte(nil), h2.Extra...)
	h2.Extra[0] = 0x02
	blk2 := block.NewBlock(h2, nil)

	if blk1.Hash() == blk2.Hash() {
		t.Fatal("test setup: the two candidate blocks must hash differently")
	}

	if kept := w.firstSealedOnParent(parent); kept != nil {
		t.Fatal("expected no candidate recorded before the first seal")
	}

	// The first seal on this parent is recorded immediately -- this is what
	// handleSealed now does before push/propose, not after the write
	// completes, so the record exists no matter how long that write takes.
	w.recordSealedOnParent(parent, blk1)
	if kept := w.firstSealedOnParent(parent); kept == nil || kept.Hash() != blk1.Hash() {
		t.Fatal("the first seal was not recorded as the kept candidate")
	}

	// A second, divergent seal on the SAME parent -- exactly the shape of a
	// parked speculative task completing while the first block's write is
	// still in flight -- must never overwrite the kept candidate.
	w.recordSealedOnParent(parent, blk2)
	if kept := w.firstSealedOnParent(parent); kept == nil || kept.Hash() != blk1.Hash() {
		t.Fatal("a later divergent seal on the same parent overwrote the kept candidate")
	}
}
