// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers BlockChain.StateAt's normal construction path (no ancient store
// attached in a test memdb, so the sealed-horizon guard never triggers).

package internal

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

func TestStateAtReturnsUsableState(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got := bc.StateAt(tx, 0)
		ibs, ok := got.(*state.IntraBlockState)
		if !ok || ibs == nil {
			t.Fatalf("StateAt(0) = %v (%T), want a non-nil *state.IntraBlockState", got, got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
