// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestWarmOverlayReaderCodeFallThroughAndClose covers the warm/cold code
// lookup split and the idempotent Close path.
func TestWarmOverlayReaderCodeFallThroughAndClose(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	cold := &countingReader{accts: map[types.Address]*account.StateAccount{}}
	r := NewWarmOverlayReader(tx, cold)

	var addr types.Address
	addr[0] = 0xC1
	codeHash := types.Hash{0x01}

	// No warm row, no cold code -> nil, nil (cold reader always answers nil).
	got, err := r.ReadAccountCode(addr, codeHash)
	if err != nil || got != nil {
		t.Fatalf("ReadAccountCode(no warm, cold miss) = %v, %v, want nil, nil", got, err)
	}

	// Seed a warm (catch-up-deployed) code row.
	code := []byte{0x60, 0x01}
	if err := tx.Put(modules.Code, codeHash[:], code); err != nil {
		t.Fatalf("seed code: %v", err)
	}
	got, err = r.ReadAccountCode(addr, codeHash)
	if err != nil || string(got) != string(code) {
		t.Fatalf("ReadAccountCode(warm) = %x, %v, want %x, nil", got, err, code)
	}

	size, err := r.ReadAccountCodeSize(addr, codeHash)
	if err != nil || size != len(code) {
		t.Fatalf("ReadAccountCodeSize = %d, %v, want %d, nil", size, err, len(code))
	}

	// Empty code hash short-circuits.
	if c, err := r.ReadAccountCode(addr, types.Hash{}); err != nil || c != nil {
		t.Fatalf("ReadAccountCode(empty hash) = %v, %v, want nil, nil", c, err)
	}

	// Close is idempotent.
	r.Close()
	r.Close()
}

// TestWarmOverlayReaderAccountStorageTombstoneAndFallThrough covers the
// three-state warm/tombstone/cold-fallthrough rule for both accounts and
// storage.
func TestWarmOverlayReaderAccountStorageTombstoneAndFallThrough(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	var addr types.Address
	addr[0] = 0xD2
	coldAcc := &account.StateAccount{Initialised: true, Nonce: 9}
	cold := &countingReader{accts: map[types.Address]*account.StateAccount{addr: coldAcc}}
	r := NewWarmOverlayReader(tx, cold)

	// Warm absent -> falls through to cold.
	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("ReadAccountData(fallthrough): %v", err)
	}
	if got == nil || got.Nonce != 9 {
		t.Fatalf("ReadAccountData(fallthrough) = %+v, want cold account", got)
	}

	// Warm tombstone (present, empty value) -> absent, no fall-through.
	if err := tx.Put(modules.Account, addr[:], nil); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	got, err = r.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("ReadAccountData(tombstone): %v", err)
	}
	if got != nil {
		t.Fatalf("ReadAccountData(tombstone) = %+v, want nil", got)
	}

	// Storage: fall-through on absent, tombstone on empty value, real value
	// on present.
	slot := types.Hash{0x03}
	sv, err := r.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatalf("ReadAccountStorage(fallthrough): %v", err)
	}
	if sv != nil {
		t.Fatalf("ReadAccountStorage(fallthrough) = %x, want nil (cold stub always nil)", sv)
	}

	ck := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes())
	if err := tx.Put(modules.Storage, ck, nil); err != nil {
		t.Fatalf("seed storage tombstone: %v", err)
	}
	sv, err = r.ReadAccountStorage(addr, &slot)
	if err != nil || sv != nil {
		t.Fatalf("ReadAccountStorage(tombstone) = %x, %v, want nil, nil", sv, err)
	}
}
