// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression for the CreateContract cache invalidation: it must still drop
// the shared cache whenever a wipe can strand a stale addr|slot entry, but
// must NOT do so for a plain deployment to a virgin address — CreateContract
// fires on every CREATE/CREATE2, so an unconditional Clear empties the
// cross-block cache several times per block.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// seedCacheEntry parks an unrelated account in the cache so we can tell a
// global Clear from "no invalidation".
func seedCacheEntry(t *testing.T, w *CachedStateWriter) types.Address {
	t.Helper()
	other := types.HexToAddress("0x000000000000000000000000000000000000f00d")
	a := account.NewAccount()
	a.Nonce = 7
	orig := account.NewAccount()
	if err := w.UpdateAccountData(other, &orig, &a); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.cache.Get(modules.Account, other.Bytes()); !ok {
		t.Fatal("seed entry not cached")
	}
	return other
}

func TestCreateContractHintedSkipsClearForVirginAddress(t *testing.T) {
	db, cache := setupTestDB(t)
	addr := types.HexToAddress("0x0000000000000000000000000000000000001111")

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewCachedStateWriter(NewPlainStateWriter(tx, tx, 1), cache)
		other := seedCacheEntry(t, w)

		// hint=false: the state layer established the address holds no
		// persisted storage, and this writer wrote none for it either.
		if err := w.CreateContractHinted(addr, false); err != nil {
			return err
		}
		if _, ok := cache.Get(modules.Account, other.Bytes()); !ok {
			t.Error("plain deployment cleared the whole cache")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateContractHintedClearsWhenStorageMayBeCached(t *testing.T) {
	db, cache := setupTestDB(t)
	addr := types.HexToAddress("0x0000000000000000000000000000000000002222")

	// (a) hint=true (persisted storage exists) must still clear.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewCachedStateWriter(NewPlainStateWriter(tx, tx, 1), cache)
		other := seedCacheEntry(t, w)
		if err := w.CreateContractHinted(addr, true); err != nil {
			return err
		}
		if _, ok := cache.Get(modules.Account, other.Bytes()); ok {
			t.Error("metamorphic recreate did not invalidate the cache")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// (b) hint=false but this writer already cached a slot for the address
	// earlier in the same block — the hint only covers PERSISTED storage,
	// so the writer's own record must still force the clear.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewCachedStateWriter(NewPlainStateWriter(tx, tx, 2), cache)
		other := seedCacheEntry(t, w)
		if err := w.WriteAccountStorage(addr, types.HexToHash("0x01"),
			*uint256.NewInt(0), *uint256.NewInt(42)); err != nil {
			return err
		}
		if err := w.CreateContractHinted(addr, false); err != nil {
			return err
		}
		if _, ok := cache.Get(modules.Account, other.Bytes()); ok {
			t.Error("same-block storage write did not force invalidation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestMayHaveCachedStorageIsConservative pins the hint itself: without a
// wipe capture (no RootComputer, or a reader that cannot enumerate) it must
// answer true, so the invalidation degrades to the old unconditional Clear
// rather than silently skipping it.
func TestMayHaveCachedStorageIsConservative(t *testing.T) {
	db, _ := setupTestDB(t)
	addr := types.HexToAddress("0x0000000000000000000000000000000000003333")

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		if !ibs.mayHaveCachedStorage(addr) {
			t.Error("no capture must be reported as may-have-storage")
		}
		// An empty capture is the provable "virgin address" case.
		ibs.wipedStorageSlots[addr] = map[types.Hash]uint256.Int{}
		if ibs.mayHaveCachedStorage(addr) {
			t.Error("empty capture must be reported as cannot-have-storage")
		}
		ibs.wipedStorageSlots[addr] = map[types.Hash]uint256.Int{
			types.HexToHash("0x01"): *uint256.NewInt(1),
		}
		if !ibs.mayHaveCachedStorage(addr) {
			t.Error("non-empty capture must be reported as may-have-storage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
