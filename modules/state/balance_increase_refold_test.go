// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regressions for the two pending-balance-increase holes found while
// auditing the balanceIncreaseTransfer revert: an increase parked BEFORE
// the snapshot must survive a revert that happens to fold it, and the
// side-effect-free existence probe must not depend on how many times it
// has been called.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

func seedAccount(t *testing.T, db kv.RwDB, addr types.Address, balance uint64) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Nonce = 1
		a.Balance = *uint256.NewInt(balance)
		orig := account.NewAccount()
		return NewPlainStateWriter(tx, tx, 1).UpdateAccountData(addr, &orig, &a)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBalanceIncreasePreSnapshotSurvivesRevert covers the shape the
// revert fix originally broke: the increase is parked BEFORE the
// snapshot, so reverting must undo the fold WITHOUT discarding the
// increase — it is still owed. The re-fold happens on the next access,
// which is what FinalizeTx/CommitBlock rely on.
//
// Real shape: B selfdestructs to an untouched beneficiary Z (parks the
// increase); control returns to A, which makes a fresh call that reads Z
// (folds) and then reverts. Z must still receive the proceeds.
func TestBalanceIncreasePreSnapshotSurvivesRevert(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000000000000000000000000000000000d3")
	const dbBalance = 1000
	seedAccount(t, db, addr, dbBalance)

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		// Parked before the snapshot: this increase is NOT part of what
		// the revert below is allowed to undo.
		ibs.AddBalance(addr, uint256.NewInt(500))

		snap := ibs.Snapshot()
		if got := ibs.GetBalance(addr); got.Uint64() != dbBalance+500 {
			t.Fatalf("in-call balance = %d, want %d", got.Uint64(), dbBalance+500)
		}
		ibs.RevertToSnapshot(snap)

		if got := ibs.GetBalance(addr); got.Uint64() != dbBalance+500 {
			t.Fatalf("post-revert balance = %d, want %d (pre-snapshot increase destroyed)",
				got.Uint64(), dbBalance+500)
		}

		// And it must actually reach the write set, not just the reader.
		w := NewNoopWriter()
		if err := ibs.FinalizeTx(&params.Rules{}, w); err != nil {
			t.Fatal(err)
		}
		if got := ibs.GetBalance(addr); got.Uint64() != dbBalance+500 {
			t.Fatalf("post-finalize balance = %d, want %d", got.Uint64(), dbBalance+500)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestExistPureIdempotentWithPendingIncrease pins ExistPure against the
// probe-count dependence: the balanceInc check used to live only in the
// nilAccounts branch, so the FIRST call (which falls through to the DB
// read) answered false and every later one answered true. ExistPure
// feeds the pre-SpuriousDragon CALL new-account surcharge, so a
// disagreement between calls is a consensus-visible gas difference.
func TestExistPureIdempotentWithPendingIncrease(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000000000000000000000000000000000e4")

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		ibs.AddBalance(addr, uint256.NewInt(1))

		first := ibs.ExistPure(addr)
		second := ibs.ExistPure(addr)
		if first != second {
			t.Fatalf("ExistPure not idempotent: first=%v second=%v", first, second)
		}
		if want := ibs.Exist(addr); first != want {
			t.Fatalf("ExistPure=%v but Exist=%v (gas charge would diverge from geth)", first, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
