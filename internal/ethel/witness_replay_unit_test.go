// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestWitnessReplayReader_StreamRoundtrip pins the on-wire format
// invariants the parallel replayer depends on. Same encoding as the
// existing WitnessStateReader writer (witness.go), reversed.
func TestWitnessReplayReader_StreamRoundtrip(t *testing.T) {
	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	slot := types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000aaaa")

	acc := &account.StateAccount{Initialised: true, Nonce: 7}
	acc.Balance.SetUint64(100)
	encodedAcct := acc.MarshalV2()
	stream := []byte{}
	stream = append(stream, byte(len(encodedAcct)))
	stream = append(stream, encodedAcct...)
	stream = append(stream, byte(1))
	stream = append(stream, 0x42)

	r := NewWitnessReplayReader(stream, nil)

	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Nonce != 7 || got.Balance.Uint64() != 100 {
		t.Fatalf("account roundtrip lost data: %+v", got)
	}

	val, err := r.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}
	if len(val) != 1 || val[0] != 0x42 {
		t.Fatalf("storage roundtrip got %x want 42", val)
	}

	if v, err := r.ReadAccountStorage(addr, &slot); err != nil || v != nil {
		t.Fatalf("past-end read should return (nil, nil): got (%x, %v)", v, err)
	}
}

// TestWitnessReplayReader_AbsentEntries pins zero-length encoding for
// "absent / empty" reads. A length-0 byte means "this read returned
// nil", keeping the stream position synchronised with the EVM access
// pattern.
func TestWitnessReplayReader_AbsentEntries(t *testing.T) {
	stream := []byte{0, 0, 0}
	r := NewWitnessReplayReader(stream, nil)

	addr := types.Address{}
	slot := types.Hash{}

	if a, _ := r.ReadAccountData(addr); a != nil {
		t.Fatalf("len=0 read should yield nil account, got %+v", a)
	}
	if v, _ := r.ReadAccountStorage(addr, &slot); v != nil {
		t.Fatalf("len=0 read should yield nil storage, got %x", v)
	}
	if a, _ := r.ReadAccountData(addr); a != nil {
		t.Fatalf("third len=0 read should still yield nil, got %+v", a)
	}
}

// TestWitnessReplayReader_CodeFromMDBX confirms the asymmetric source
// for code: witness omits it, the reader pulls from the supplied
// kv.Tx's Code table. The reader verifies keccak256(value) matches
// the codeHash key, so the test stores under the correct hash.
func TestWitnessReplayReader_CodeFromMDBX(t *testing.T) {
	bytecode := []byte{0x60, 0x80, 0x60, 0x40, 0x52}
	codeHash := crypto.Keccak256Hash(bytecode)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Put("Code", codeHash[:], bytecode); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()

	r := NewWitnessReplayReader(nil, roTx)
	addr := types.Address{}

	got, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bytecode) {
		t.Fatalf("code roundtrip got %x want %x", got, bytecode)
	}

	sz, err := r.ReadAccountCodeSize(addr, codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if sz != len(bytecode) {
		t.Fatalf("size got %d want %d", sz, len(bytecode))
	}
}

// TestWitnessCapturingWriter_RecordsBothOldAndNew pins the contract
// the parallel worker depends on: every UpdateAccountData / Storage
// write records BOTH (old, new) values — old via inner ChangeSetWriter,
// new via private maps fed to EncodeAccount/StorageChanges.
func TestWitnessCapturingWriter_RecordsBothOldAndNew(t *testing.T) {
	addr := types.HexToAddress("0x2222222222222222222222222222222222222222")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000005")

	old := &account.StateAccount{Initialised: true, Nonce: 1}
	old.Balance.SetUint64(50)
	new1 := &account.StateAccount{Initialised: true, Nonce: 2}
	new1.Balance.SetUint64(75)

	w := NewWitnessCapturingWriter()
	if err := w.UpdateAccountData(addr, old, new1); err != nil {
		t.Fatal(err)
	}

	cs, err := w.ChangeSetWriter().GetAccountChanges()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Len() != 1 {
		t.Fatalf("expected 1 account change, got %d", cs.Len())
	}
	if v := w.AccountNewValue(addr); len(v) == 0 {
		t.Fatal("AccountNewValue empty for updated account")
	}

	oldSto := uint256.NewInt(0)
	newSto := uint256.NewInt(0xDEADBEEF)
	if err := w.WriteAccountStorage(addr, slot, *oldSto, *newSto); err != nil {
		t.Fatal(err)
	}
	stoCS, err := w.ChangeSetWriter().GetStorageChanges()
	if err != nil {
		t.Fatal(err)
	}
	if stoCS.Len() != 1 {
		t.Fatalf("expected 1 storage change, got %d", stoCS.Len())
	}
	if v := w.StorageNewValue(addr, slot); len(v) == 0 {
		t.Fatal("StorageNewValue empty for updated slot")
	}
}

// TestWitnessCapturingWriter_PostState pins that PostState() exposes the
// captured changeset (accounts/storage/wiped) the mobile overlay folds in —
// account update, storage write, delete-as-nil, and CreateContract wipe.
func TestWitnessCapturingWriter_PostState(t *testing.T) {
	a1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	a2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	wiped := types.HexToAddress("0x3333333333333333333333333333333333333333")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")

	old := &account.StateAccount{Initialised: true, Nonce: 1}
	old.Balance.SetUint64(10)
	newA := &account.StateAccount{Initialised: true, Nonce: 2}
	newA.Balance.SetUint64(99)

	w := NewWitnessCapturingWriter()
	if err := w.UpdateAccountData(a1, old, newA); err != nil {
		t.Fatal(err)
	}
	if err := w.DeleteAccount(a2, old); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(a1, slot, *uint256.NewInt(0), *uint256.NewInt(0x42)); err != nil {
		t.Fatal(err)
	}
	if err := w.CreateContract(wiped); err != nil {
		t.Fatal(err)
	}

	ps := w.PostState()
	if ps == nil {
		t.Fatal("PostState nil")
	}
	if len(ps.Accounts[a1]) == 0 {
		t.Error("a1 account bytes missing from PostState")
	}
	if v, ok := ps.Accounts[a2]; !ok || v != nil {
		t.Errorf("a2 should be present with nil (deleted) value, got ok=%v v=%x", ok, v)
	}
	inner, ok := ps.Storage[a1]
	if !ok || len(inner[slot]) == 0 {
		t.Errorf("a1 storage slot missing from PostState: ok=%v", ok)
	}
	if _, ok := ps.Wiped[wiped]; !ok {
		t.Error("wiped address missing from PostState.Wiped")
	}
}

// TestWitnessCapturingWriter_DeleteAccount pins that DeleteAccount
// records a nil new-value (so EncodeAccountChanges emits a deletion
// marker), without disturbing the old-value capture path.
func TestWitnessCapturingWriter_DeleteAccount(t *testing.T) {
	addr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	old := &account.StateAccount{Initialised: true, Nonce: 9}
	old.Balance.SetUint64(123)

	w := NewWitnessCapturingWriter()
	if err := w.DeleteAccount(addr, old); err != nil {
		t.Fatal(err)
	}
	if v := w.AccountNewValue(addr); v != nil {
		t.Fatalf("DeleteAccount must record nil new-value, got %x", v)
	}
	cs, err := w.ChangeSetWriter().GetAccountChanges()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Len() != 1 {
		t.Fatalf("DeleteAccount must record a change, got %d", cs.Len())
	}
}
