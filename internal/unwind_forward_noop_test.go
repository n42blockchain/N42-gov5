// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestForwardImportNoop pins the read-only pre-check unwindForReimport runs
// before opening a write transaction: it must say "no-op" exactly where
// unwindForReimportTx returns at its top, and nowhere else.
func TestForwardImportNoop(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	head := types.Hash{0xaa}
	sibling := types.Hash{0xbb}

	check := func(name string, n uint64, parent types.Hash, want bool) {
		t.Helper()
		got, err := bc.forwardImportNoop(n, parent)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: noop=%v, want %v", name, got, want)
		}
		// The write-path decision must agree.
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			_, _, txNoop, terr := appliedHeadForUnwind(tx, n, parent)
			if terr != nil {
				return terr
			}
			if txNoop != want {
				t.Fatalf("%s: appliedHeadForUnwind noop=%v, want %v", name, txNoop, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing stored: nothing to unwind.
	check("empty", 5, head, true)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteQMDBApplied(tx, 10, head)
	}); err != nil {
		t.Fatal(err)
	}
	check("forward", 11, head, true)           // extends the applied head
	check("gap", 13, sibling, true)            // parent not applied yet
	check("sibling child", 11, sibling, false) // parent is not the applied head
	check("same height", 10, types.Hash{0x09}, false)
	check("below", 7, types.Hash{0x06}, false)
}
