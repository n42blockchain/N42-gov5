// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestSelfdestruct8246 pins EIP-8246 self-beneficiary semantics: a
// same-tx-created account loses code/storage/nonce but keeps its
// balance; a pre-existing account is fully untouched.
func TestSelfdestruct8246(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	ibs := New(NewPlainState(tx, 1))
	addr := types.Address{0x82, 0x46}
	slot := types.Hash{0x01}

	// Same-tx creation with balance, code, storage and nonce.
	ibs.CreateAccount(addr, true)
	bal := uint256.NewInt(1_000_000)
	ibs.AddBalance(addr, bal)
	ibs.SetCode(addr, []byte{0x60, 0x00})
	val := uint256.NewInt(42)
	ibs.SetState(addr, &slot, *val)
	ibs.SetNonce(addr, 7)

	ibs.Selfdestruct8246(addr)

	if got := ibs.GetBalance(addr); got.Cmp(bal) != 0 {
		t.Fatalf("balance = %s, want %s (must survive)", got, bal)
	}
	if code := ibs.GetCode(addr); len(code) != 0 {
		t.Fatalf("code survived: %x", code)
	}
	if nonce := ibs.GetNonce(addr); nonce != 0 {
		t.Fatalf("nonce = %d, want 0", nonce)
	}
	var out uint256.Int
	ibs.GetState(addr, &slot, &out)
	if !out.IsZero() {
		t.Fatalf("storage survived: %s", out.String())
	}

	// Pre-existing account (no created flag): full no-op.
	ibs2 := New(NewPlainState(tx, 1))
	pre := types.Address{0xAA}
	ibs2.AddBalance(pre, uint256.NewInt(500))
	ibs2.SetNonce(pre, 3)
	// Simulate "pre-existing" by finalizing the created flag away: use a
	// fresh IBS view where the object is loaded, not created.
	ibs2.Selfdestruct8246(pre)
	if got := ibs2.GetBalance(pre); got.Uint64() != 500 {
		t.Fatalf("pre-existing balance changed: %s", got)
	}
	if ibs2.GetNonce(pre) != 3 {
		t.Fatalf("pre-existing nonce changed")
	}
}
