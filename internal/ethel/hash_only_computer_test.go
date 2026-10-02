// Copyright 2022-2026 The N42 Authors

// hash_only_computer_test.go — cover HashOnlyComputer.ComputeRoot: put a
// new account with zero CodeHash (must normalize to emptyCodeHash), put
// storage slots (including a value with a short trimmed form and a zero
// value that must delete), delete an account and confirm its storage is
// swept, and confirm the returned root is always the zero hash since no
// trie is computed.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestHashOnlyComputer_ComputeRoot_PutAndNormalize(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	hc := NewHashOnlyComputer(tx)

	var addr types.Address
	addr[19] = 0x01
	acct := &account.StateAccount{Initialised: true, Nonce: 5}
	acct.Balance = *uint256.NewInt(1000)
	// CodeHash left zero: ComputeRoot must normalize it to emptyCodeHash.

	var slotA, slotB types.Hash
	slotA[31] = 0x01
	slotB[31] = 0x02
	storage := map[types.Address]map[types.Hash]*uint256.Int{
		addr: {
			slotA: uint256.NewInt(0x1234),
			slotB: uint256.NewInt(0), // zero value -> delete path
		},
	}

	root, err := hc.ComputeRoot(map[types.Address]*account.StateAccount{addr: acct}, storage)
	if err != nil {
		t.Fatalf("ComputeRoot: %v", err)
	}
	if root != (types.Hash{}) {
		t.Fatalf("expected zero root from HashOnlyComputer, got %x", root)
	}

	addrHash := testKeccak256(addr[:])
	v, err := tx.GetOne(modules.HashedAccounts, addrHash)
	if err != nil || v == nil {
		t.Fatalf("expected HashedAccounts entry, err=%v v=%v", err, v)
	}
	// ComputeRoot normalizes a zero CodeHash to the canonical empty-code
	// hash before marshaling, so the stored bytes must match an account
	// explicitly carrying that hash (and differ from the zero-hash form).
	emptyCH := testKeccak256(nil)
	var wantAcct account.StateAccount
	wantAcct.Initialised = acct.Initialised
	wantAcct.Nonce = acct.Nonce
	wantAcct.Balance = acct.Balance
	copy(wantAcct.CodeHash[:], emptyCH)
	want := wantAcct.MarshalV2()
	if string(v) != string(want) {
		t.Fatalf("expected stored account to carry normalized emptyCodeHash;\ngot  %x\nwant %x", v, want)
	}

	slotAHash := testKeccak256(slotA[:])
	key := append(append([]byte{}, addrHash...), slotAHash...)
	sv, err := tx.GetOne(modules.HashedStorage, key)
	if err != nil || sv == nil {
		t.Fatalf("expected slotA stored, err=%v v=%v", err, sv)
	}

	slotBHash := testKeccak256(slotB[:])
	key2 := append(append([]byte{}, addrHash...), slotBHash...)
	sv2, err := tx.GetOne(modules.HashedStorage, key2)
	if err != nil {
		t.Fatalf("GetOne slotB: %v", err)
	}
	if sv2 != nil {
		t.Fatalf("expected slotB (zero value) to be absent, got %x", sv2)
	}
}

func TestHashOnlyComputer_ComputeRoot_DeleteSweepsStorage(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	hc := NewHashOnlyComputer(tx)

	var addr types.Address
	addr[19] = 0x02
	acct := &account.StateAccount{Initialised: true, Nonce: 1}
	acct.Balance = *uint256.NewInt(1)

	var slot types.Hash
	slot[31] = 0x09
	storage := map[types.Address]map[types.Hash]*uint256.Int{
		addr: {slot: uint256.NewInt(7)},
	}
	if _, err := hc.ComputeRoot(map[types.Address]*account.StateAccount{addr: acct}, storage); err != nil {
		t.Fatalf("seed ComputeRoot: %v", err)
	}

	addrHash := testKeccak256(addr[:])
	if v, _ := tx.GetOne(modules.HashedAccounts, addrHash); v == nil {
		t.Fatal("expected account to exist before delete")
	}

	// Now delete the account (nil value) -- must remove HashedAccounts entry
	// and sweep all HashedStorage rows for that address.
	if _, err := hc.ComputeRoot(map[types.Address]*account.StateAccount{addr: nil}, nil); err != nil {
		t.Fatalf("delete ComputeRoot: %v", err)
	}

	if v, _ := tx.GetOne(modules.HashedAccounts, addrHash); v != nil {
		t.Fatalf("expected HashedAccounts entry removed, got %x", v)
	}
	slotHash := testKeccak256(slot[:])
	key := append(append([]byte{}, addrHash...), slotHash...)
	if v, _ := tx.GetOne(modules.HashedStorage, key); v != nil {
		t.Fatalf("expected storage swept for deleted account, got %x", v)
	}
}

func TestHashOnlyComputer_ComputeRoot_Empty(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	hc := NewHashOnlyComputer(tx)
	root, err := hc.ComputeRoot(nil, nil)
	if err != nil {
		t.Fatalf("ComputeRoot(nil, nil): %v", err)
	}
	if root != (types.Hash{}) {
		t.Fatalf("expected zero root, got %x", root)
	}
}

func testKeccak256(b []byte) []byte {
	return crypto.Keccak256(b)
}
