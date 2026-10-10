// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// A pipelined execution opens its transaction before its parent's write
// flushes and evicts the parent's entries. Through a plain lookup source the
// evicted entry reads as absent; the refreshing source retries it through a
// fresh transaction and finds it.
func TestLookupSourceRefreshingSeesEvictedEntry(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	rc.SetEvictLag(0)
	ctx := context.Background()
	a := types.BytesToAddress([]byte{0x42})
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{a: testAcct(3, 9)}, nil); err != nil {
		t.Fatal(err)
	}
	lagging, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lagging.Rollback()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		rc.SetCold(tx)
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)

	plain := state.NewPlainStateReader(lagging)
	got, err := NewQMDBStateReader(NewLookupSource(rc, lagging), plain, QMDBReadOn).ReadAccountData(a)
	if err != nil || got != nil {
		t.Fatalf("plain source over a lagging transaction: %+v %v, want absent", got, err)
	}
	before := LookupRefreshes()
	got, err = NewQMDBStateReader(NewLookupSourceRefreshing(rc, lagging, db), plain, QMDBReadOn).ReadAccountData(a)
	if err != nil || got == nil || got.Nonce != 3 {
		t.Fatalf("refreshing source: %+v %v, want nonce 3", got, err)
	}
	if LookupRefreshes() == before {
		t.Fatal("the refresh was not counted")
	}
}
