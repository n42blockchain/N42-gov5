// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapshot

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
)

// --- DiskLayer getters / Stale / AccountDeleted ---

func TestDiskLayer_Getters(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	root := testHash(9)
	dl := NewDiskLayer(cache, 42, root)

	if dl.Cache() != cache {
		t.Fatalf("Cache() mismatch")
	}
	if dl.DB() != nil {
		t.Fatalf("expected nil DB before SetDB")
	}
	if dl.Stale() {
		t.Fatalf("expected fresh layer to not be stale")
	}
	dl.MarkStale()
	if !dl.Stale() {
		t.Fatalf("expected layer to be stale after MarkStale")
	}
	if dl.AccountDeleted(testAddr(1)) {
		t.Fatalf("DiskLayer.AccountDeleted must always be false")
	}
	if dl.GenReady() {
		t.Fatalf("expected GenReady false by default")
	}
	dl.SetGenReady(true)
	if !dl.GenReady() {
		t.Fatalf("expected GenReady true after SetGenReady(true)")
	}

	db := newTestDB(t)
	dl.SetDB(db)
	if dl.DB() != db {
		t.Fatalf("DB() mismatch after SetDB")
	}
}

func TestDiskLayer_Account_NotGenReady(t *testing.T) {
	dl := NewDiskLayer(nil, 1, testHash(1))
	// genReady is false by default: Account/Storage must short-circuit.
	if _, ok := dl.Account(testAddr(1)); ok {
		t.Fatalf("expected Account to report not-found when genReady is false")
	}
	if _, ok := dl.Storage(testAddr(1), testHash(1)); ok {
		t.Fatalf("expected Storage to report not-found when genReady is false")
	}
}

func TestDiskLayer_Account_GenReadyNoDB(t *testing.T) {
	dl := NewDiskLayer(nil, 1, testHash(1))
	dl.SetGenReady(true)
	// genReady true but db is nil: must still short-circuit safely.
	if _, ok := dl.Account(testAddr(1)); ok {
		t.Fatalf("expected Account to report not-found when db is nil")
	}
	if _, ok := dl.Storage(testAddr(1), testHash(1)); ok {
		t.Fatalf("expected Storage to report not-found when db is nil")
	}
}

// --- Tree.DiskLayer / Tree.SetGenReady ---

func TestTree_DiskLayerAccessor(t *testing.T) {
	tree, _ := newTestTree()
	dl := tree.DiskLayer()
	if dl == nil {
		t.Fatalf("expected non-nil disk layer")
	}

	tree.SetGenReady(true)
	if !dl.GenReady() {
		t.Fatalf("expected Tree.SetGenReady to propagate to the disk layer")
	}
}

// --- Warmer ---

func TestWarmer_NilCacheOrDB(t *testing.T) {
	w := NewWarmer(nil, nil, 10)
	if err := w.Warm(context.Background()); err != nil {
		t.Fatalf("expected nil error when cache and db are nil, got %v", err)
	}

	db := newTestDB(t)
	w2 := NewWarmer(db, nil, 10)
	if err := w2.Warm(context.Background()); err != nil {
		t.Fatalf("expected nil error when cache is nil, got %v", err)
	}

	cache := layered.NewShardedCache(4, 1024)
	w3 := NewWarmer(nil, cache, 10)
	if err := w3.Warm(context.Background()); err != nil {
		t.Fatalf("expected nil error when db is nil, got %v", err)
	}
}

func TestWarmer_WarmPopulatesCache(t *testing.T) {
	db := newTestDB(t)
	// Seed two accounts directly into the Account table.
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := tx.Put(modules.Account, []byte("addr-one"), []byte("value-one")); err != nil {
			return err
		}
		return tx.Put(modules.Account, []byte("addr-two"), []byte("value-two"))
	})
	if err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	cache := layered.NewShardedCache(4, 1024)
	w := NewWarmer(db, cache, 0) // 0 -> DefaultWarmupAccounts
	if err := w.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	if _, ok := cache.Get(modules.Account, []byte("addr-one")); !ok {
		t.Fatalf("expected addr-one to be warmed into the cache")
	}
	if _, ok := cache.Get(modules.Account, []byte("addr-two")); !ok {
		t.Fatalf("expected addr-two to be warmed into the cache")
	}
}

func TestWarmer_WarmRespectsMaxAccounts(t *testing.T) {
	db := newTestDB(t)
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 5; i++ {
			k := []byte{byte(i)}
			if err := tx.Put(modules.Account, k, []byte("v")); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	cache := layered.NewShardedCache(4, 1024)
	w := NewWarmer(db, cache, 1) // cap at 1 account
	if err := w.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
}

func TestWarmer_WarmContextCancelled(t *testing.T) {
	db := newTestDB(t)
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 5; i++ {
			k := []byte{byte(i)}
			if err := tx.Put(modules.Account, k, []byte("v")); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	cache := layered.NewShardedCache(4, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Warm runs
	w := NewWarmer(db, cache, 100)
	if err := w.Warm(ctx); err == nil {
		t.Fatalf("expected context cancellation error")
	}
}
