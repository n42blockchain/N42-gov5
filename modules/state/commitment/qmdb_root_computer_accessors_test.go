// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers small QMDBRootComputer accessors left at 0%: RootScheme, ValueSource,
// ResidentTwigLeaves, and the MDBX-backed index wiring (UseMDBXIndex/SetIndexTx)
// exercised through two real blocks so the MDBX index path actually drives
// Put/Get instead of the default in-RAM map.

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
)

func TestQMDBRootComputerSimpleAccessors(t *testing.T) {
	rc := NewQMDBRootComputer()
	if got := rc.RootScheme(); got != state.RootSchemeQMDB {
		t.Fatalf("RootScheme() = %v, want %v", got, state.RootSchemeQMDB)
	}
	if rc.ValueSource() == nil {
		t.Fatal("ValueSource() must not be nil")
	}
	if n := rc.ResidentTwigLeaves(); n < 0 {
		t.Fatalf("ResidentTwigLeaves() = %d, want >= 0", n)
	}
}

func TestQMDBRootComputerMDBXIndexWiring(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()

	addr1, addr2 := qmAddr(5), qmAddr(6)

	// UseMDBXIndex must be called BEFORE the first ComputeRoot/LoadFrom, with
	// the first batch's tx.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.UseMDBXIndex(tx)
		rc.SetCold(tx)
		if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{addr1: qmAcct(1, 100)}, nil); err != nil {
			return err
		}
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)

	// Second batch: re-point the MDBX index at the new tx via SetIndexTx, and
	// verify addr1 (written last batch, now index-resident only in MDBX, not
	// this batch's in-RAM state) is still found through the re-pointed index,
	// alongside the newly-written addr2.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.SetIndexTx(tx)
		rc.SetCold(tx)
		if _, found, _ := rc.Lookup(qmdb.Hash(AccountKeyHash(addr1)), tx); !found {
			t.Fatal("expected addr1 to be found through the MDBX-backed index after SetIndexTx")
		}
		if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{addr2: qmAcct(1, 200)}, nil); err != nil {
			return err
		}
		if _, found, _ := rc.Lookup(qmdb.Hash(AccountKeyHash(addr2)), tx); !found {
			t.Fatal("expected addr2 to be found right after being written")
		}
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)
}
