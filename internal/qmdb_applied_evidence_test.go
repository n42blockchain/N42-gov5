// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for HasAppliedBlock — the applied-state evidence probe behind the
// import-gated vote. The live failure it pins down: six validators voted a
// forked leader's block into a CommitQC because "imported" was judged by body
// presence / a nil insert error, neither of which proves the block's state was
// ever executed here.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestHasAppliedBlock(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	// Applied lineage: h8 <- h9 <- h10 (marker at h10). s10 is a stored
	// same-height sibling of h10 that was never executed here.
	mkHdr := func(n uint64, parent types.Hash, tag byte) *block.Header {
		return &block.Header{
			Number:     uint256.NewInt(n),
			ParentHash: parent,
			Difficulty: uint256.NewInt(1),
			Extra:      []byte{tag},
		}
	}
	h8 := mkHdr(8, types.Hash{0x08}, 1)
	h9 := mkHdr(9, h8.Hash(), 1)
	h10 := mkHdr(10, h9.Hash(), 1)
	s10 := mkHdr(10, h9.Hash(), 2) // sibling: same parent, different content

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []*block.Header{h8, h9, h10, s10} {
		rawdb.WriteHeader(tx, h)
	}
	// Committed prefix reaches 9; 10 is applied but not yet committed.
	if err := rawdb.WriteCanonicalHash(tx, h8.Hash(), 8); err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteCanonicalHash(tx, h9.Hash(), 9); err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteQMDBApplied(tx, 10, h10.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		hash types.Hash
		num  uint64
		want bool
	}{
		{"applied head (marker block)", h10.Hash(), 10, true},
		{"canonical below marker", h9.Hash(), 9, true},
		{"canonical deeper", h8.Hash(), 8, true},
		{"stored sibling never executed", s10.Hash(), 10, false},
		{"above the applied head", types.Hash{0x11}, 11, false},
		{"unknown hash at applied height", types.Hash{0xEE}, 10, false},
	}
	for _, c := range cases {
		if got := bc.HasAppliedBlock(c.hash, c.num); got != c.want {
			t.Errorf("%s: HasAppliedBlock = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestHasAppliedBlock_NoMarkerFallsBackToBody: chains without an applied
// marker keep the pre-gate behavior (body presence).
func TestHasAppliedBlock_NoMarkerFallsBackToBody(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	h := &block.Header{Number: uint256.NewInt(5), Difficulty: uint256.NewInt(1)}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rawdb.WriteHeader(tx, h)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if !bc.HasAppliedBlock(h.Hash(), 5) {
		t.Error("stored header without a marker should count as applied (fallback)")
	}
	if bc.HasAppliedBlock(types.Hash{0xAB}, 5) {
		t.Error("missing header without a marker must not count as applied")
	}
}
