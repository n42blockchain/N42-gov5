// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestHashedReadCacheInvalidation verifies the invalidate-refill contract:
// a cached read serves the memoized bytes until the key is invalidated, after
// which the next read refills from the DB with the new value.
func TestHashedReadCacheInvalidation(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	var addrHash [32]byte
	copy(addrHash[:], crypto.Keccak256(addr[:]))

	acc1 := &account.StateAccount{Initialised: true, Nonce: 7}
	acc1.CodeHash = types.BytesToHash(crypto.Keccak256(nil))
	if err := tx.Put(modules.HashedAccounts, addrHash[:], acc1.MarshalV2()); err != nil {
		t.Fatal(err)
	}

	cache := NewHashedReadCache()
	r := NewHashedStateReader(tx)
	r.SetCache(cache)

	got, err := r.ReadAccountData(addr)
	if err != nil || got == nil || got.Nonce != 7 {
		t.Fatalf("first read: %+v err=%v", got, err)
	}

	// Mutate the DB directly (simulating the TrieRootComputer's forward write).
	acc2 := &account.StateAccount{Initialised: true, Nonce: 8}
	acc2.CodeHash = acc1.CodeHash
	if err := tx.Put(modules.HashedAccounts, addrHash[:], acc2.MarshalV2()); err != nil {
		t.Fatal(err)
	}

	// Without invalidation the cache still serves nonce 7 (documented hazard —
	// this is exactly why every writer must call InvalidateAccount).
	got, _ = r.ReadAccountData(addr)
	if got.Nonce != 7 {
		t.Fatalf("expected stale cached nonce 7 before invalidate, got %d", got.Nonce)
	}

	cache.InvalidateAccount(addrHash)
	got, _ = r.ReadAccountData(addr)
	if got.Nonce != 8 {
		t.Fatalf("after invalidate: want nonce 8, got %d", got.Nonce)
	}

	// Absence is cached too: unknown account misses once then serves nil from
	// cache; a later write + invalidate makes it appear.
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	var addrHash2 [32]byte
	copy(addrHash2[:], crypto.Keccak256(addr2[:]))
	if got, _ := r.ReadAccountData(addr2); got != nil {
		t.Fatal("expected absent account")
	}
	if err := tx.Put(modules.HashedAccounts, addrHash2[:], acc1.MarshalV2()); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ReadAccountData(addr2); got != nil {
		t.Fatal("absence should still be cached")
	}
	cache.InvalidateAccount(addrHash2)
	if got, _ := r.ReadAccountData(addr2); got == nil || got.Nonce != 7 {
		t.Fatal("after invalidate the account should appear")
	}
}

// TestHashedReadCacheStorage covers the storage tier including PurgeStorageAll.
func TestHashedReadCacheStorage(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	addr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	slot := types.HexToHash("0x01")
	var composite [64]byte
	copy(composite[:32], crypto.Keccak256(addr[:]))
	copy(composite[32:], crypto.Keccak256(slot[:]))

	if err := tx.Put(modules.HashedStorage, composite[:], []byte{0xAA}); err != nil {
		t.Fatal(err)
	}

	cache := NewHashedReadCache()
	r := NewHashedStateReader(tx)
	r.SetCache(cache)

	v, err := r.ReadAccountStorage(addr, &slot)
	if err != nil || len(v) != 1 || v[0] != 0xAA {
		t.Fatalf("first storage read: %x err=%v", v, err)
	}

	if err := tx.Put(modules.HashedStorage, composite[:], []byte{0xBB}); err != nil {
		t.Fatal(err)
	}
	v, _ = r.ReadAccountStorage(addr, &slot)
	if v[0] != 0xAA {
		t.Fatal("expected cached 0xAA before invalidate")
	}

	cache.InvalidateStorage(composite)
	v, _ = r.ReadAccountStorage(addr, &slot)
	if v[0] != 0xBB {
		t.Fatalf("after invalidate: want BB got %x", v)
	}

	// Per-account generation purge (account wipe path): bumping the gen makes
	// every cached slot of THAT account miss, without touching others.
	if err := tx.Put(modules.HashedStorage, composite[:], []byte{0xCC}); err != nil {
		t.Fatal(err)
	}
	var ah [32]byte
	copy(ah[:], composite[:32])
	cache.PurgeAccountStorage(ah)
	v, _ = r.ReadAccountStorage(addr, &slot)
	if v[0] != 0xCC {
		t.Fatalf("after PurgeAccountStorage: want CC got %x", v)
	}

	// A different account's cached slot must survive the purge above.
	addrB := types.HexToAddress("0x4444444444444444444444444444444444444444")
	var compB [64]byte
	copy(compB[:32], crypto.Keccak256(addrB[:]))
	copy(compB[32:], crypto.Keccak256(slot[:]))
	if err := tx.Put(modules.HashedStorage, compB[:], []byte{0xD1}); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.ReadAccountStorage(addrB, &slot); v[0] != 0xD1 {
		t.Fatal("prime B")
	}
	if err := tx.Put(modules.HashedStorage, compB[:], []byte{0xD2}); err != nil {
		t.Fatal(err)
	}
	cache.PurgeAccountStorage(ah) // wipe A again — B must stay cached
	if v, _ := r.ReadAccountStorage(addrB, &slot); v[0] != 0xD1 {
		t.Fatalf("B should still serve cached D1, got %x", v)
	}
}
