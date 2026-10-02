// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestG52LastIntermediateRootAndDirtySet pins the plain getters that expose
// the cached results of the most recent IntermediateRoot / root-computer
// call, which nothing else in the suite reads back out.
func TestG52LastIntermediateRootAndDirtySet(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)

	if root, ok := ibs.LastIntermediateRoot(); ok || root != (types.Hash{}) {
		t.Fatalf("fresh IBS should report no cached root, got root=%v ok=%v", root, ok)
	}

	want := types.HexToHash("0x1234")
	ibs.lastIntermediateRoot, ibs.lastIntermediateRootSet = want, true
	if got, ok := ibs.LastIntermediateRoot(); !ok || got != want {
		t.Fatalf("LastIntermediateRoot mismatch: got=%v ok=%v want=%v", got, ok, want)
	}

	if accounts, storage := ibs.LastRootDirtySet(); accounts != nil || storage != nil {
		t.Fatalf("fresh IBS should report nil dirty sets, got accounts=%v storage=%v", accounts, storage)
	}
}

// TestG52BeforeStateRootNilAndPopulatedSnapshot covers both branches of
// BeforeStateRoot: no snapshot configured (early nil-hash return) and a
// populated writable snapshot with account+code data that must hash.
func TestG52BeforeStateRootNilAndPopulatedSnapshot(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		if h, err := ibs.BeforeStateRoot(); err != nil || h != (types.Hash{}) {
			t.Fatalf("no-snap BeforeStateRoot should be a no-op zero hash, got h=%v err=%v", h, err)
		}

		addr := types.HexToAddress("0x00000000000000000000000000000000000000e1")
		ibs.CreateAccount(addr, true)
		ibs.SetCode(addr, []byte{0x60, 0x01, 0x60, 0x02})

		ibs.snap = NewWritableSnapshot()
		h1, err := ibs.BeforeStateRoot()
		if err != nil {
			t.Fatalf("BeforeStateRoot with snap set: %v", err)
		}

		// Calling again with the same inputs must be deterministic.
		h2, err := ibs.BeforeStateRoot()
		if err != nil {
			t.Fatalf("second BeforeStateRoot call: %v", err)
		}
		if h1 != h2 {
			t.Fatalf("BeforeStateRoot should be deterministic over unchanged state: h1=%v h2=%v", h1, h2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
