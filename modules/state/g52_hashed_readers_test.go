// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// g52CodeSourceStub is a minimal CodeSource returning a fixed code for one
// address and an error for lookups it wasn't told about.
type g52CodeSourceStub struct {
	addr types.Address
	code []byte
}

func (s *g52CodeSourceStub) GetCode(address types.Address) ([]byte, error) {
	if address == s.addr {
		return s.code, nil
	}
	return nil, nil
}

// TestG52HashedStateReaderStorageAndCode drives HashedStateReader's
// ReadAccountStorage, ReadAccountCode (both the codeSrc fast path and the
// modules.Code fallback) and ReadAccountCodeSize -- none of which any
// existing test reaches.
func TestG52HashedStateReaderStorageAndCode(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x00000000000000000000000000000000000000f5")
	addrHash := crypto.Keccak256Hash(addr[:])
	slot := types.HexToHash("0x09")
	slotHash := crypto.Keccak256Hash(slot[:])
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x01}
	codeHash := crypto.Keccak256Hash(code)

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		composite := make([]byte, 64)
		copy(composite[:32], addrHash[:])
		copy(composite[32:], slotHash[:])
		if err := tx.Put(modules.HashedStorage, composite, []byte{0x2a}); err != nil {
			return err
		}
		return tx.Put(modules.Code, codeHash[:], code)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		r := NewHashedStateReader(tx)

		got, err := r.ReadAccountStorage(addr, &slot)
		if err != nil || len(got) != 1 || got[0] != 0x2a {
			t.Fatalf("ReadAccountStorage = %x, %v; want [0x2a], nil", got, err)
		}

		// Empty codeHash short-circuits to nil, nil.
		if got, err := r.ReadAccountCode(addr, types.Hash{}); err != nil || got != nil {
			t.Fatalf("empty codeHash should read nil code, got %x err=%v", got, err)
		}

		// Fallback path: no codeSrc set, reads modules.Code directly.
		got2, err := r.ReadAccountCode(addr, codeHash)
		if err != nil || string(got2) != string(code) {
			t.Fatalf("ReadAccountCode fallback = %x, %v; want %x", got2, err, code)
		}
		if size, err := r.ReadAccountCodeSize(addr, codeHash); err != nil || size != len(code) {
			t.Fatalf("ReadAccountCodeSize = %d, %v; want %d", size, err, len(code))
		}

		// codeSrc fast path: a fresh reader (uncached) whose codeSrc returns
		// the right bytes must verify the hash and skip modules.Code.
		other := types.HexToAddress("0x00000000000000000000000000000000000000f6")
		otherCodeHash := crypto.Keccak256Hash([]byte{0x11, 0x22})
		r2 := NewHashedStateReader(tx)
		r2.SetCodeSource(&g52CodeSourceStub{addr: other, code: []byte{0x11, 0x22}})
		got3, err := r2.ReadAccountCode(other, otherCodeHash)
		if err != nil || string(got3) != "\x11\x22" {
			t.Fatalf("ReadAccountCode via codeSrc = %x, %v; want 1122", got3, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52HashedHistoricalReaderCodeAndStorageDelegation checks the thin
// delegating wrappers on HashedHistoricalReader: SetCodeSource, SetCache,
// ReadAccountStorage, ReadAccountCode and ReadAccountCodeSize all forward to
// the embedded tip base reader.
func TestG52HashedHistoricalReaderCodeAndStorageDelegation(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x00000000000000000000000000000000000000f7")
	addrHash := crypto.Keccak256Hash(addr[:])
	slot := types.HexToHash("0x0a")
	slotHash := crypto.Keccak256Hash(slot[:])
	code := []byte{0x60, 0x03}
	codeHash := crypto.Keccak256Hash(code)

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		composite := make([]byte, 64)
		copy(composite[:32], addrHash[:])
		copy(composite[32:], slotHash[:])
		if err := tx.Put(modules.HashedStorage, composite, []byte{0x07}); err != nil {
			return err
		}
		return tx.Put(modules.Code, codeHash[:], code)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		r := NewHashedHistoricalReader(tx, 1)
		cache := NewHashedReadCache()
		r.SetCache(cache)
		r.SetCodeSource(nil)

		got, err := r.ReadAccountStorage(addr, &slot)
		if err != nil || len(got) != 1 || got[0] != 0x07 {
			t.Fatalf("delegated ReadAccountStorage = %x, %v; want [0x07]", got, err)
		}
		gotCode, err := r.ReadAccountCode(addr, codeHash)
		if err != nil || string(gotCode) != string(code) {
			t.Fatalf("delegated ReadAccountCode = %x, %v; want %x", gotCode, err, code)
		}
		if size, err := r.ReadAccountCodeSize(addr, codeHash); err != nil || size != len(code) {
			t.Fatalf("delegated ReadAccountCodeSize = %d, %v; want %d", size, err, len(code))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52HashedReadCacheCodeAndStats exercises GetCode/PutCode,
// StatsAndReset's hit/miss accounting, and PurgeAll's wholesale clear of the
// invalidatable tiers (code and keccak memos are left intact by design).
func TestG52HashedReadCacheCodeAndStats(t *testing.T) {
	c := NewHashedReadCache()

	codeHash := crypto.Keccak256Hash([]byte{0xaa, 0xbb})
	if _, ok := c.GetCode(codeHash); ok {
		t.Fatal("expected a miss before PutCode")
	}
	c.PutCode(codeHash, []byte{0xaa, 0xbb})
	got, ok := c.GetCode(codeHash)
	if !ok || string(got) != "\xaa\xbb" {
		t.Fatalf("GetCode after PutCode = %x, %v; want aabb, true", got, ok)
	}

	addrHash := crypto.Keccak256Hash(types.HexToAddress("0x00000000000000000000000000000000000000f8").Bytes())
	if _, _, ok := c.GetAccount(addrHash); ok {
		t.Fatal("expected an account miss before PutAccount")
	}
	c.PutAccount(addrHash, []byte{0x01})
	if _, _, ok := c.GetAccount(addrHash); !ok {
		t.Fatal("expected an account hit before PurgeAll")
	}

	accHit, accMiss, _, _, codeHit, codeMiss := c.StatsAndReset()
	if accHit == 0 || accMiss == 0 || codeHit == 0 || codeMiss == 0 {
		t.Fatalf("expected non-zero hit/miss across all tiers before reset: accHit=%d accMiss=%d codeHit=%d codeMiss=%d",
			accHit, accMiss, codeHit, codeMiss)
	}
	// StatsAndReset zeroes the counters: a second call reads all zero.
	accHit2, accMiss2, _, _, _, _ := c.StatsAndReset()
	if accHit2 != 0 || accMiss2 != 0 {
		t.Fatalf("StatsAndReset should zero counters, got accHit=%d accMiss=%d", accHit2, accMiss2)
	}

	c.PurgeAll()
	if _, _, ok := c.GetAccount(addrHash); ok {
		t.Fatal("PurgeAll should drop cached accounts")
	}
	// Code cache is immutable/content-addressed and must survive PurgeAll.
	if _, ok := c.GetCode(codeHash); !ok {
		t.Fatal("PurgeAll must not drop the immutable code cache")
	}
}
