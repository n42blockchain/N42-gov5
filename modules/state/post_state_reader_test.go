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
	"github.com/n42blockchain/N42/params"
)

// A build that extends a block this node sealed but has not written must read
// that block's effects: a credited balance, a created account, a removed one,
// a written slot, and fall through to the store for everything untouched.
func TestPostStateReaderLayersTheSealedBlock(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	coinbase := types.HexToAddress("0x10000000000000000000000000000000000000c0")
	fresh := types.HexToAddress("0x10000000000000000000000000000000000000f1")
	doomed := types.HexToAddress("0x10000000000000000000000000000000000000d0")
	untouched := types.HexToAddress("0x10000000000000000000000000000000000000ee")
	slot := types.HexToHash("0x01")
	db := memdb.NewTestDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)
		orig := account.NewAccount()
		for _, a := range []struct {
			addr    types.Address
			balance uint64
		}{{coinbase, 10}, {doomed, 7}, {untouched, 3}} {
			acc := account.NewAccount()
			acc.Nonce = 1
			acc.Balance.SetUint64(a.balance)
			if err := writer.UpdateAccountData(a.addr, &orig, &acc); err != nil {
				return err
			}
		}
		return writer.WriteAccountStorage(doomed, slot, *uint256.NewInt(0), *uint256.NewInt(9))
	}); err != nil {
		t.Fatal(err)
	}

	err := db.View(context.Background(), func(tx kv.Tx) error {
		base := NewPlainStateReader(tx)
		ibs := New(base)
		ibs.AddBalance(coinbase, uint256.NewInt(5))
		ibs.CreateAccount(fresh, false)
		ibs.SetNonce(fresh, 4)
		ibs.SetState(coinbase, &slot, *uint256.NewInt(0x1234))
		ibs.Selfdestruct(doomed)
		ibs.SubBalance(SystemAddress, uint256.NewInt(0)) // creates it, empty
		if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()); err != nil {
			return err
		}

		post := CapturePostState(ibs)
		r := NewPostStateReader(post, base)

		got, err := r.ReadAccountData(coinbase)
		if err != nil || got == nil || got.Balance.Uint64() != 15 {
			t.Fatalf("coinbase after the sealed block: %+v %v, want balance 15", got, err)
		}
		if got, _ := r.ReadAccountData(fresh); got == nil || got.Nonce != 4 {
			t.Fatalf("created account not visible: %+v", got)
		}
		if got, _ := r.ReadAccountData(doomed); got != nil {
			t.Fatalf("selfdestructed account still visible: %+v", got)
		}
		if got, _ := r.ReadAccountData(untouched); got == nil || got.Balance.Uint64() != 3 {
			t.Fatalf("untouched account must fall through to the store: %+v", got)
		}
		if v, _ := r.ReadAccountStorage(coinbase, &slot); len(v) != 2 || v[0] != 0x12 || v[1] != 0x34 {
			t.Fatalf("written slot = %x, want 1234", v)
		}
		if v, _ := r.ReadAccountStorage(doomed, &slot); v != nil {
			t.Fatalf("slot of a selfdestructed account = %x, want zero", v)
		}
		other := types.HexToHash("0x02")
		if v, _ := r.ReadAccountStorage(coinbase, &other); v != nil {
			t.Fatalf("unwritten slot must fall through to the store (empty): %x", v)
		}
		// An account the block created but left empty (a value-zero system
		// call's caller) reads as absent, exactly as the store would show it.
		if got, _ := r.ReadAccountData(SystemAddress); got != nil {
			t.Fatalf("empty created account must read as absent: %+v", got)
		}
		// The snapshot must be independent of the state it came from.
		ibs.AddBalance(coinbase, uint256.NewInt(100))
		if got, _ := r.ReadAccountData(coinbase); got.Balance.Uint64() != 15 {
			t.Fatalf("snapshot followed a later mutation: %d", got.Balance.Uint64())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
