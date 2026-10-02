// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestG52SoftFinaliseDirtiesWithoutClearingObjects pins that SoftFinalise
// promotes journal-dirtied addresses into stateObjectsDirty, lifts wipes
// into priorTxWipes, and clears the journal/refund -- but (unlike
// FinalizeTx) never calls MakeWriteSet, so the state objects live on for
// a following tx in the same block.
func TestG52SoftFinaliseDirtiesWithoutClearingObjects(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		addr := types.HexToAddress("0x00000000000000000000000000000000000000d1")
		ibs.CreateAccount(addr, true)
		ibs.AddBalance(addr, uint256.NewInt(10))
		ibs.AddRefund(21000)

		if _, dirty := ibs.stateObjectsDirty[addr]; dirty {
			t.Fatal("addr should not be dirty before SoftFinalise")
		}

		ibs.SoftFinalise()

		if _, dirty := ibs.stateObjectsDirty[addr]; !dirty {
			t.Fatal("SoftFinalise should promote the journal-dirtied addr into stateObjectsDirty")
		}
		if ibs.journal.length() != 0 {
			t.Fatalf("SoftFinalise should clear the journal, got %d entries", ibs.journal.length())
		}
		if ibs.GetRefund() != 0 {
			t.Fatalf("SoftFinalise should clear the refund counter, got %d", ibs.GetRefund())
		}
		if _, stillExists := ibs.stateObjects[addr]; !stillExists {
			t.Fatal("SoftFinalise must not remove the state object, only FinalizeTx/CommitBlock do")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52BalanceIncreaseSet checks that only untransferred pending balance
// increases are reported, and a transferred one is excluded.
func TestG52BalanceIncreaseSet(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000d2")
	other := types.HexToAddress("0x00000000000000000000000000000000000000d3")

	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		ibs.AddBalance(addr, uint256.NewInt(100))
		ibs.AddBalance(other, uint256.NewInt(200))
		// Materializing `other` folds its pending increase (transferred=true),
		// so it must drop out of BalanceIncreaseSet.
		ibs.GetBalance(other)

		set := ibs.BalanceIncreaseSet()
		if v, ok := set[addr]; !ok || v.Uint64() != 100 {
			t.Fatalf("expected untransferred increase of 100 for addr, got %v ok=%v", v, ok)
		}
		if _, ok := set[other]; ok {
			t.Fatal("transferred increase for other must not appear in BalanceIncreaseSet")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52PrepareReadableSnapshotAndSetSnapshot round-trips a writable
// snapshot through RLP, feeds the bytes to PrepareReadableSnapshot, and
// separately exercises SetSnapshot's direct account/storage index build.
func TestG52PrepareReadableSnapshotAndSetSnapshot(t *testing.T) {
	ibs, db := g51NewTestIBS(t)
	_ = db

	addr := types.HexToAddress("0x00000000000000000000000000000000000000d4")
	key := types.HexToHash("0x01")

	w := NewWritableSnapshot()
	acc := account.NewAccount()
	acc.Nonce = 7
	w.AddAccount(addr, &acc)
	w.AddStorage(addr, &key, []byte{0x2a})

	data, err := rlp.EncodeToBytes(w)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}

	if err := ibs.PrepareReadableSnapshot(data); err != nil {
		t.Fatalf("PrepareReadableSnapshot: %v", err)
	}
	if ibs.snap == nil {
		t.Fatal("PrepareReadableSnapshot should populate sdb.snap")
	}
	if got, err := ibs.snap.ReadAccountData(addr); err != nil || got == nil || got.Nonce != 7 {
		t.Fatalf("round-tripped account mismatch: got=%v err=%v", got, err)
	}

	if err := ibs.PrepareReadableSnapshot([]byte{0xff}); err == nil {
		t.Fatal("expected PrepareReadableSnapshot to surface a decode error on garbage input")
	}

	// SetSnapshot builds the accounts/storage index maps directly from
	// Items rather than going through RLP decode.
	manual := &Snapshot{Items: Items{
		{Key: addr[:], Value: []byte{0x01}},
		{Key: append(append([]byte{}, addr[:]...), key[:]...), Value: []byte{0x02}},
	}}
	ibs.SetSnapshot(manual)
	if len(manual.accounts) != 1 || len(manual.storage) != 1 {
		t.Fatalf("SetSnapshot should split Items into one account entry and one storage entry, got accounts=%d storage=%d",
			len(manual.accounts), len(manual.storage))
	}
}

// TestG52Selfdestruct6780CreatedInSameTx covers the "destroy the creation"
// branch: a same-tx-created account is fully wiped and its balance zeroed,
// with a selfdestructChange journaled.
func TestG52Selfdestruct6780CreatedInSameTx(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		addr := types.HexToAddress("0x00000000000000000000000000000000000000d5")
		beneficiary := types.HexToAddress("0x00000000000000000000000000000000000000d6")
		ibs.CreateAccount(addr, true)
		ibs.AddBalance(addr, uint256.NewInt(50))

		if !ibs.WasCreatedInCurrentTx(addr) {
			t.Fatal("addr should be reported as created in the current tx")
		}

		snap := ibs.Snapshot()
		ibs.Selfdestruct6780(addr, beneficiary)

		if !ibs.GetBalance(addr).IsZero() {
			t.Fatal("created-in-tx selfdestruct6780 should zero the balance")
		}

		ibs.RevertToSnapshot(snap)
		if got := ibs.GetBalance(addr); got.Uint64() != 50 {
			t.Fatalf("revert should restore pre-selfdestruct balance, got %d", got.Uint64())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52Selfdestruct6780PreExisting covers the "transfer only" branch for
// an account NOT created in the current tx: balance moves, code/storage
// survive (caller does the AddBalance to beneficiary; here beneficiary ==
// addr is also exercised as the self-to-self halving case).
func TestG52Selfdestruct6780PreExisting(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000d7")

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Balance = *uint256.NewInt(1000)
		orig := account.NewAccount()
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(addr, &orig, &a)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		if ibs.WasCreatedInCurrentTx(addr) {
			t.Fatal("a DB-loaded account must not report created-in-current-tx")
		}

		other := types.HexToAddress("0x00000000000000000000000000000000000000d8")
		ibs.Selfdestruct6780(addr, other)
		if !ibs.GetBalance(addr).IsZero() {
			t.Fatal("transfer-only selfdestruct6780 should clear the source balance")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Self-to-self halving branch: caller doubles the balance via AddBalance
	// before calling Selfdestruct6780(addr, addr); the function must halve
	// it back to the original value.
	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		doubled := ibs.GetBalance(addr)
		doubled = uint256.NewInt(0).Set(doubled)
		ibs.AddBalance(addr, uint256.NewInt(1000)) // mirror the double-credit opSelfdestruct performs
		ibs.Selfdestruct6780(addr, addr)
		if got := ibs.GetBalance(addr); got.Uint64() != 1000 {
			t.Fatalf("self-to-self selfdestruct6780 should halve back to original, got %d", got.Uint64())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52DirtySetSizesAndPrint exercises the diagnostic accounting of
// DirtySetSizes (empty vs non-empty dirty accounts) and smoke-tests Print,
// which only writes to the global logger and has no return value to assert.
func TestG52DirtySetSizesAndPrint(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))

		empty := types.HexToAddress("0x00000000000000000000000000000000000000d9")
		nonEmpty := types.HexToAddress("0x00000000000000000000000000000000000000da")

		ibs.CreateAccount(empty, true)
		ibs.CreateAccount(nonEmpty, true)
		ibs.AddBalance(nonEmpty, uint256.NewInt(1))
		ibs.SoftFinalise()

		accounts, emptyCount := ibs.DirtySetSizes()
		if accounts != 2 {
			t.Fatalf("expected 2 dirty accounts, got %d", accounts)
		}
		if emptyCount != 1 {
			t.Fatalf("expected 1 empty (nonce=0,balance=0) dirty account, got %d", emptyCount)
		}

		// Print must not panic over a populated state.
		ibs.Print()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
