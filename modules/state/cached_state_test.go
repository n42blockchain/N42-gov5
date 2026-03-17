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

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func setupTestDB(t *testing.T) (kv.RwDB, *layered.ShardedCache) {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	cache := layered.NewShardedCache(16, 1000)
	return db, cache
}

func TestCachedStateReader_ReadAccountData(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// Write an account.
	err := db.Update(ctx, func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Nonce = 42
		a.Balance = *uint256.NewInt(1000)
		orig := account.NewAccount()
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(addr, &orig, &a)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read with cached reader.
	err = db.View(ctx, func(tx kv.Tx) error {
		inner := NewPlainStateReader(tx)
		reader := NewCachedStateReader(inner, cache)

		// First read — cache miss, should hit DB.
		a, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a == nil {
			t.Fatal("expected account, got nil")
		}
		if a.Nonce != 42 {
			t.Errorf("nonce: want 42, got %d", a.Nonce)
		}

		// Second read — should hit cache.
		a2, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a2.Nonce != 42 {
			t.Errorf("cached nonce: want 42, got %d", a2.Nonce)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify cache was populated.
	hits, _, _ := cache.Stats()
	if hits == 0 {
		t.Error("expected cache hits > 0")
	}
}

func TestCachedStateReader_NegativeCache(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xdeadbeef00000000000000000000000000000000")

	err := db.View(ctx, func(tx kv.Tx) error {
		inner := NewPlainStateReader(tx)
		reader := NewCachedStateReader(inner, cache)

		// Read non-existent account — should be cached as nil.
		a, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a != nil {
			t.Fatal("expected nil for non-existent account")
		}

		// Second read — should hit negative cache.
		a2, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a2 != nil {
			t.Fatal("expected nil from negative cache")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	hits, _, _ := cache.Stats()
	if hits == 0 {
		t.Error("expected cache hit for negative cache")
	}
}

func TestCachedStateReader_ReadAccountStorage(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xaaaa000000000000000000000000000000000001")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	// Write storage.
	err := db.Update(ctx, func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 1)
		val := uint256.NewInt(999)
		orig := uint256.NewInt(0)
		return w.WriteAccountStorage(addr, 1, &key, orig, val)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read with cached reader.
	err = db.View(ctx, func(tx kv.Tx) error {
		inner := NewPlainStateReader(tx)
		reader := NewCachedStateReader(inner, cache)

		v, err := reader.ReadAccountStorage(addr, 1, &key)
		if err != nil {
			return err
		}
		if v == nil {
			t.Fatal("expected storage value, got nil")
		}

		// Second read — cache hit.
		v2, err := reader.ReadAccountStorage(addr, 1, &key)
		if err != nil {
			return err
		}
		if v2 == nil {
			t.Fatal("expected cached storage value")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCachedStateWriter_UpdateInvalidatesCache(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xbbbb000000000000000000000000000000000001")

	// Write initial account.
	a1 := account.NewAccount()
	a1.Nonce = 1
	a1.Balance = *uint256.NewInt(100)
	orig := account.NewAccount()

	err := db.Update(ctx, func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(addr, &orig, &a1)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read to populate cache.
	err = db.View(ctx, func(tx kv.Tx) error {
		reader := NewCachedStateReader(NewPlainStateReader(tx), cache)
		_, err := reader.ReadAccountData(addr)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Update account with cached writer.
	a2 := account.NewAccount()
	a2.Nonce = 2
	a2.Balance = *uint256.NewInt(200)

	err = db.Update(ctx, func(tx kv.RwTx) error {
		inner := NewPlainStateWriter(tx, tx, 2)
		w := NewCachedStateWriter(inner, cache)
		return w.UpdateAccountData(addr, &a1, &a2)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read again — cache should have updated value.
	err = db.View(ctx, func(tx kv.Tx) error {
		reader := NewCachedStateReader(NewPlainStateReader(tx), cache)
		a, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a.Nonce != 2 {
			t.Errorf("nonce after update: want 2, got %d", a.Nonce)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCachedStateWriter_DeleteInvalidatesCache(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xcccc000000000000000000000000000000000001")

	// Write and cache.
	a := account.NewAccount()
	a.Nonce = 1
	aOrig := account.NewAccount()
	err := db.Update(ctx, func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(addr, &aOrig, &a)
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = db.View(ctx, func(tx kv.Tx) error {
		reader := NewCachedStateReader(NewPlainStateReader(tx), cache)
		_, _ = reader.ReadAccountData(addr)
		return nil
	})

	// Delete with cached writer.
	err = db.Update(ctx, func(tx kv.RwTx) error {
		inner := NewPlainStateWriter(tx, tx, 2)
		w := NewCachedStateWriter(inner, cache)
		return w.DeleteAccount(addr, &a)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Cache should be invalidated.
	v, ok := cache.Get(modules.Account, addr.Bytes())
	if ok && v != nil {
		t.Error("expected cache to be invalidated after delete")
	}
}

func TestCachedStateReader_NilCache(t *testing.T) {
	db, _ := setupTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xdddd000000000000000000000000000000000001")

	// nil cache should work transparently.
	err := db.View(ctx, func(tx kv.Tx) error {
		reader := NewCachedStateReader(NewPlainStateReader(tx), nil)
		a, err := reader.ReadAccountData(addr)
		if err != nil {
			return err
		}
		if a != nil {
			t.Error("expected nil account")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
