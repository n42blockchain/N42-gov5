// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestMVStateReader_PassesThrough(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	ev := NewEVMStateView(NewMVStateView(mv, base, 5, 0))

	// Seed base.
	addr := mkAddr(0xa)
	slot := mkHash(0xb)
	baseR := newMVMockReader()
	baseR.accounts[addr] = &account.StateAccount{
		Nonce: 7, Balance: *uint256.NewInt(100), Initialised: true,
	}
	baseR.storage[addr] = map[types.Hash][]byte{slot: uint256.NewInt(99).Bytes()}
	mvBase := NewMVBaseFromStateReader(baseR)
	ev = NewEVMStateView(NewMVStateView(mv, mvBase, 5, 0))

	r := NewMVStateReader(ev)

	// Account.
	acct, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if acct == nil || acct.Nonce != 7 || acct.Balance.Uint64() != 100 {
		t.Errorf("account: %+v", acct)
	}

	// Storage.
	v, err := r.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}
	want := uint256.NewInt(99).Bytes()
	if !bytes.Equal(v, want) {
		t.Errorf("storage: got %x want %x", v, want)
	}

	// Missing account returns nil.
	missing := mkAddr(0xff)
	acct, err = r.ReadAccountData(missing)
	if err != nil {
		t.Fatal(err)
	}
	if acct != nil {
		t.Errorf("missing: %+v", acct)
	}
}

func TestMVStateWriter_PassesThrough(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	ev := NewEVMStateView(NewMVStateView(mv, base, 3, 0))
	w := NewMVStateWriter(ev)

	addr := mkAddr(0xa)
	slot := mkHash(0xb)

	// Write account.
	acct := &account.StateAccount{
		Nonce: 1, Balance: *uint256.NewInt(42), Initialised: true,
	}
	copy(acct.CodeHash[:], bytes.Repeat([]byte{0xcc}, 32))
	if err := w.UpdateAccountData(addr, nil, acct); err != nil {
		t.Fatal(err)
	}

	// Write storage.
	if err := w.WriteAccountStorage(addr, &slot, nil, uint256.NewInt(777)); err != nil {
		t.Fatal(err)
	}

	// Write code.
	code := []byte{0x60, 0x80, 0x60, 0x40}
	codeHash := mkHash(0xaa)
	if err := w.UpdateAccountCode(addr, codeHash, code); err != nil {
		t.Fatal(err)
	}

	// Self-destruct via CreateContract (wipe marker).
	addr2 := mkAddr(0xb)
	if err := w.CreateContract(addr2); err != nil {
		t.Fatal(err)
	}

	// Flush to MV.
	ev.Inner().FlushWrites()

	// Verify by reading back with a later-txIdx view.
	readView := NewEVMStateView(NewMVStateView(mv, base, 100, 0))
	gotAcct, _ := readView.ReadAccount(addr)
	if gotAcct == nil || gotAcct.Nonce != 1 || gotAcct.Balance.Uint64() != 42 {
		t.Errorf("account roundtrip: %+v", gotAcct)
	}
	gotStor, _ := readView.ReadStorage(addr, slot)
	if gotStor.Uint64() != 777 {
		t.Errorf("storage roundtrip: %s", gotStor.String())
	}
	gotCode, _ := readView.ReadCode(codeHash)
	if !bytes.Equal(gotCode, code) {
		t.Errorf("code roundtrip: %x want %x", gotCode, code)
	}
	if !readView.IsAddressWiped(addr2) {
		t.Errorf("CreateContract should have wiped addr2")
	}
}

func TestMVStateWriter_DeleteAccount(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	addr := mkAddr(0xa)
	// Tx 1 writes account.
	ev1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	NewMVStateWriter(ev1).UpdateAccountData(addr, nil,
		&account.StateAccount{Nonce: 5, Initialised: true})
	ev1.Inner().FlushWrites()

	// Tx 3 deletes.
	ev3 := NewEVMStateView(NewMVStateView(mv, base, 3, 0))
	NewMVStateWriter(ev3).DeleteAccount(addr, nil)
	ev3.Inner().FlushWrites()

	// Tx 5 reads — should see deletion (nil).
	got, _ := NewEVMStateView(NewMVStateView(mv, base, 5, 0)).ReadAccount(addr)
	if got != nil {
		t.Errorf("post-delete: got %+v want nil", got)
	}
}

// TestMVStateIO_EndToEnd demonstrates the whole adapter loop in one
// go: write some state via MVStateWriter, then read it back via
// MVStateReader, verifying the values match.
func TestMVStateIO_EndToEnd(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	addr := mkAddr(0xa)
	slot := mkHash(0x1)

	// Tx 0 writes.
	ev0 := NewEVMStateView(NewMVStateView(mv, base, 0, 0))
	w0 := NewMVStateWriter(ev0)
	w0.UpdateAccountData(addr, nil, &account.StateAccount{
		Nonce: 3, Balance: *uint256.NewInt(500), Initialised: true,
	})
	w0.WriteAccountStorage(addr, &slot, nil, uint256.NewInt(42))
	ev0.Inner().FlushWrites()

	// Tx 1 reads via StateReader interface.
	ev1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	r1 := NewMVStateReader(ev1)

	acct, err := r1.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Nonce != 3 || acct.Balance.Uint64() != 500 {
		t.Errorf("account: %+v", acct)
	}

	slotVal, err := r1.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(slotVal, uint256.NewInt(42).Bytes()) {
		t.Errorf("slot: %x want 0x2a", slotVal)
	}

	// Size check for missing codeHash returns 0.
	sz, _ := r1.ReadAccountCodeSize(addr, types.Hash{})
	if sz != 0 {
		t.Errorf("codeSize of empty hash: %d want 0", sz)
	}
}
