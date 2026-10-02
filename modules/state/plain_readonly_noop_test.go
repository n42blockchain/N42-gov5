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

// TestPlainStateWriteMethodsAndCodeSize covers the PlainState writer-side
// no-ops (UpdateAccountData/DeleteAccount/UpdateAccountCode always return
// nil -- PlainState is a read-only historical snapshot), the btree-backed
// WriteAccountStorage/CreateContract pair, and ReadAccountCodeSize.
func TestPlainStateWriteMethodsAndCodeSize(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		s := NewPlainState(tx, 10)
		s.SetTrace(true)
		s.SetTrace(false)
		if s.GetBlockNr() != 10 {
			t.Fatalf("GetBlockNr = %d, want 10", s.GetBlockNr())
		}
		s.SetBlockNr(20)
		if s.GetBlockNr() != 20 {
			t.Fatalf("GetBlockNr after SetBlockNr = %d, want 20", s.GetBlockNr())
		}

		addr := types.HexToAddress("0x1000000000000000000000000000000000000041")
		orig := account.NewAccount()
		acc := account.NewAccount()

		if err := s.UpdateAccountData(addr, &orig, &acc); err != nil {
			t.Fatalf("UpdateAccountData = %v, want nil", err)
		}
		if err := s.DeleteAccount(addr, &orig); err != nil {
			t.Fatalf("DeleteAccount = %v, want nil", err)
		}
		if err := s.UpdateAccountCode(addr, types.Hash{0x01}, []byte{0x60}); err != nil {
			t.Fatalf("UpdateAccountCode = %v, want nil", err)
		}

		slot := types.HexToHash("0x02")
		if err := s.WriteAccountStorage(addr, slot, uint256.Int{}, *uint256.NewInt(5)); err != nil {
			t.Fatalf("WriteAccountStorage: %v", err)
		}
		if _, ok := s.storage[addr]; !ok {
			t.Fatal("WriteAccountStorage did not populate the per-address btree")
		}

		if err := s.CreateContract(addr); err != nil {
			t.Fatalf("CreateContract: %v", err)
		}
		if _, ok := s.storage[addr]; ok {
			t.Fatal("CreateContract did not clear the per-address btree")
		}

		// Unset code hash on an otherwise-empty PlainState: no code row, no
		// code source -> ReadAccountCodeSize = 0, nil.
		size, err := s.ReadAccountCodeSize(addr, types.Hash{0x03})
		if err != nil || size != 0 {
			t.Fatalf("ReadAccountCodeSize = %d, %v, want 0, nil", size, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
