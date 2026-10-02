// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestNewStateHistoryReaderCurrentStateFast covers the cursor-opening
// constructor and the "current state present" fast path for account,
// storage, and code reads (no history fallback needed).
func TestNewStateHistoryReaderCurrentStateFast(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	addr := types.HexToAddress("0x1000000000000000000000000000000000000031")
	slot := types.HexToHash("0x01")
	code := []byte{0x60, 0x01}
	codeHash := types.BytesToHash(emptyCodeHashButNot(code))

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)
		orig := account.NewAccount()
		acc := account.NewAccount()
		acc.Nonce = 5
		acc.CodeHash = codeHash
		if err := writer.UpdateAccountData(addr, &orig, &acc); err != nil {
			return err
		}
		if err := writer.UpdateAccountCode(addr, codeHash, code); err != nil {
			return err
		}
		return writer.WriteAccountStorage(addr, slot, uint256.Int{}, *uint256.NewInt(99))
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		r, err := NewStateHistoryReader(tx, tx, 1)
		if err != nil {
			t.Fatalf("NewStateHistoryReader: %v", err)
		}

		r.SetBlockNumber(2)

		acc, err := r.ReadAccountData(addr)
		if err != nil {
			t.Fatalf("ReadAccountData: %v", err)
		}
		if acc == nil || acc.Nonce != 5 {
			t.Fatalf("ReadAccountData = %+v, want nonce 5", acc)
		}

		sv, err := r.ReadAccountStorage(addr, &slot)
		if err != nil {
			t.Fatalf("ReadAccountStorage: %v", err)
		}
		if len(sv) == 0 {
			t.Fatal("ReadAccountStorage returned empty for a written slot")
		}

		gotCode, err := r.ReadAccountCode(addr, codeHash)
		if err != nil {
			t.Fatalf("ReadAccountCode: %v", err)
		}
		if string(gotCode) != string(code) {
			t.Fatalf("ReadAccountCode = %x, want %x", gotCode, code)
		}

		size, err := r.ReadAccountCodeSize(addr, codeHash)
		if err != nil || size != len(code) {
			t.Fatalf("ReadAccountCodeSize = %d, %v, want %d", size, err, len(code))
		}

		// Empty code hash short-circuits without a lookup.
		if c, err := r.ReadAccountCode(addr, types.BytesToHash(emptyCodeHash)); err != nil || c != nil {
			t.Fatalf("ReadAccountCode(emptyCodeHash) = %v, %v, want nil, nil", c, err)
		}

		// GetOne: empty bucket name returns (nil, nil); a real bucket
		// delegates to the underlying Getter.
		if v, err := r.GetOne("", []byte("k")); err != nil || v != nil {
			t.Fatalf("GetOne(empty bucket) = %v, %v, want nil, nil", v, err)
		}
		if v, err := r.GetOne(modules.Account, addr[:]); err != nil || len(v) == 0 {
			t.Fatalf("GetOne(Account) = %v, %v, want non-empty", v, err)
		}

		r.Rollback()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// emptyCodeHashButNot returns a stand-in keccak-shaped hash for non-empty
// test code without importing crypto here a second time.
func emptyCodeHashButNot(code []byte) []byte {
	h := make([]byte, 32)
	copy(h, code)
	h[31] = 0xFF // guarantee it differs from the real emptyCodeHash
	return h
}
