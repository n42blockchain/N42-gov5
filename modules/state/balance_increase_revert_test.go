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
)

// TestBalanceIncreaseTransferRevertOnLoadedObject pins the revert of a
// pending balance increase that was folded into an object materialized
// FROM THE DB. setStateObject adds the increase into the object's
// balance; the object insertion itself is not journaled (unlike the
// createObject path), so a revert that only cleared the transferred flag
// left the live object carrying DB+increase — a phantom balance visible
// to later same-block reads and committable by a later write.
func TestBalanceIncreaseTransferRevertOnLoadedObject(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x00000000000000000000000000000000000000b1")
	const dbBalance = 1000

	// Pre-existing account in the DB: the object must be LOADED, not created.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Nonce = 1
		a.Balance = *uint256.NewInt(dbBalance)
		orig := account.NewAccount()
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(addr, &orig, &a)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		snap := ibs.Snapshot()

		// Credit without reading first: this parks a balanceInc entry and
		// creates no state object (the pre-SpuriousDragon SELFDESTRUCT
		// beneficiary shape, and the coinbase-credit shape).
		ibs.AddBalance(addr, uint256.NewInt(500))

		// Now read it: getStateObject materializes from the DB and
		// setStateObject folds the pending increase in.
		if got := ibs.GetBalance(addr); got.Uint64() != dbBalance+500 {
			t.Fatalf("pre-revert balance = %d, want %d", got.Uint64(), dbBalance+500)
		}

		ibs.RevertToSnapshot(snap)

		if got := ibs.GetBalance(addr); got.Uint64() != dbBalance {
			t.Fatalf("post-revert balance = %d, want %d (phantom balance survived the revert)",
				got.Uint64(), dbBalance)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBalanceIncreaseTransferRevertOnCreatedObject guards the other
// path: for an object created in-block the object itself is removed by
// its own journal entry, so the balance undo must not double-apply or
// resurrect anything.
func TestBalanceIncreaseTransferRevertOnCreatedObject(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000c2")

	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		snap := ibs.Snapshot()

		ibs.AddBalance(addr, uint256.NewInt(700))
		ibs.CreateAccount(addr, false)
		if got := ibs.GetBalance(addr); got.Uint64() != 700 {
			t.Fatalf("pre-revert balance = %d, want 700", got.Uint64())
		}

		ibs.RevertToSnapshot(snap)

		if got := ibs.GetBalance(addr); !got.IsZero() {
			t.Fatalf("post-revert balance = %d, want 0", got.Uint64())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
