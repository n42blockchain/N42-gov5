// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
)

func addrOf(b byte) types.Address {
	var a types.Address
	a[0] = b
	return a
}

func slotOf(b byte) types.Hash {
	var h types.Hash
	h[0] = b
	return h
}

// acctBytes builds V2 account bytes with the given balance/nonce.
func acctBytes(bal uint64, nonce uint64) []byte {
	a := account.NewAccount()
	a.Balance.SetUint64(bal)
	a.Nonce = nonce
	return a.MarshalV2()
}

func psAcct(addr types.Address, bal, nonce uint64) *ethel.PostState {
	return &ethel.PostState{
		Accounts: map[types.Address][]byte{addr: acctBytes(bal, nonce)},
		Storage:  map[types.Address]map[types.Hash][]byte{},
		Wiped:    map[types.Address]struct{}{},
	}
}

func TestOverlay_ReadsLatestValue(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(1)
	o.apply(10, psAcct(a, 100, 1))
	o.apply(11, psAcct(a, 250, 2))

	raw, at, ok := o.account(a)
	if !ok {
		t.Fatal("account not found in overlay")
	}
	if at != 11 {
		t.Errorf("lastBlock = %d, want 11", at)
	}
	acc, exists := decodeAccount(raw)
	if !exists {
		t.Fatal("decode failed")
	}
	if acc.Balance.Uint64() != 250 || acc.Nonce != 2 {
		t.Errorf("got balance=%d nonce=%d, want 250/2", acc.Balance.Uint64(), acc.Nonce)
	}
}

func TestOverlay_StorageRoundtrip(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(2)
	val := uint256.NewInt(0xdeadbeef)
	vb := make([]byte, val.ByteLen())
	val.WriteToSlice(vb)
	o.apply(5, &ethel.PostState{
		Accounts: map[types.Address][]byte{},
		Storage: map[types.Address]map[types.Hash][]byte{
			a: {slotOf(7): vb},
		},
		Wiped: map[types.Address]struct{}{},
	})
	got, at, ok := o.storageAt(a, slotOf(7))
	if !ok || at != 5 {
		t.Fatalf("storage miss or wrong block: ok=%v at=%d", ok, at)
	}
	var gv uint256.Int
	gv.SetBytes(got)
	if gv.Cmp(val) != 0 {
		t.Errorf("storage value mismatch: got %s want %s", gv.String(), val.String())
	}
}

func TestOverlay_PrunesAgedOutBlocks(t *testing.T) {
	o := newMobileOverlay(3) // tiny window for the test
	a1, a2 := addrOf(1), addrOf(2)
	// block 1 sets a1; blocks 2,3,4 set a2. With retention 3 and head=4,
	// cutoff = 4-3 = 1 → block 1 evicted, a1 (last-written at 1) dropped.
	o.apply(1, psAcct(a1, 10, 1))
	o.apply(2, psAcct(a2, 20, 1))
	o.apply(3, psAcct(a2, 30, 2))
	o.apply(4, psAcct(a2, 40, 3))

	if _, _, ok := o.account(a1); ok {
		t.Error("a1 should have been pruned (aged out at block 1)")
	}
	raw, at, ok := o.account(a2)
	if !ok || at != 4 {
		t.Fatalf("a2 missing or wrong block: ok=%v at=%d", ok, at)
	}
	acc, _ := decodeAccount(raw)
	if acc.Balance.Uint64() != 40 {
		t.Errorf("a2 balance = %d, want 40", acc.Balance.Uint64())
	}
}

func TestOverlay_LastWriterSurvivesPrune(t *testing.T) {
	o := newMobileOverlay(3)
	a := addrOf(9)
	// a is set at block 1 AND re-set at block 4. When block 1 ages out
	// (head=4, cutoff=1), the cell must NOT be deleted because its last
	// writer is block 4, not 1.
	o.apply(1, psAcct(a, 11, 1))
	o.apply(2, psAcct(addrOf(2), 1, 1))
	o.apply(3, psAcct(addrOf(3), 1, 1))
	o.apply(4, psAcct(a, 99, 5))

	raw, at, ok := o.account(a)
	if !ok {
		t.Fatal("a was wrongly pruned despite a block-4 rewrite")
	}
	if at != 4 {
		t.Errorf("lastBlock = %d, want 4", at)
	}
	acc, _ := decodeAccount(raw)
	if acc.Balance.Uint64() != 99 {
		t.Errorf("balance = %d, want 99", acc.Balance.Uint64())
	}
}

func TestOverlay_DeletedAccountReadsAbsent(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(4)
	o.apply(10, psAcct(a, 100, 1))
	// block 11 deletes the account (nil account bytes).
	o.apply(11, &ethel.PostState{
		Accounts: map[types.Address][]byte{a: nil},
		Storage:  map[types.Address]map[types.Hash][]byte{},
		Wiped:    map[types.Address]struct{}{},
	})
	raw, _, ok := o.account(a)
	if !ok {
		t.Fatal("deleted account should still be a window hit (absent), not a miss")
	}
	if _, exists := decodeAccount(raw); exists {
		t.Error("deleted account should decode as absent")
	}
}

func TestOverlay_WipeDropsPriorStorage(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(5)
	// block 1 sets slot 1; block 2 is a CreateContract wipe + sets slot 2.
	// After block 2, slot 1 must be gone (wiped), slot 2 present.
	o.apply(1, &ethel.PostState{
		Accounts: map[types.Address][]byte{},
		Storage:  map[types.Address]map[types.Hash][]byte{a: {slotOf(1): {0x11}}},
		Wiped:    map[types.Address]struct{}{},
	})
	o.apply(2, &ethel.PostState{
		Accounts: map[types.Address][]byte{},
		Storage:  map[types.Address]map[types.Hash][]byte{a: {slotOf(2): {0x22}}},
		Wiped:    map[types.Address]struct{}{a: {}},
	})
	if _, _, ok := o.storageAt(a, slotOf(1)); ok {
		t.Error("slot 1 should have been wiped by the block-2 CreateContract")
	}
	if v, _, ok := o.storageAt(a, slotOf(2)); !ok || len(v) != 1 || v[0] != 0x22 {
		t.Errorf("slot 2 should be present post-wipe: ok=%v v=%v", ok, v)
	}
}

func TestOverlay_MissReturnsNotOk(t *testing.T) {
	o := newMobileOverlay(900)
	if _, _, ok := o.account(addrOf(123)); ok {
		t.Error("untouched address must be a miss (→ fall back to proof)")
	}
}

// A non-forward apply (blockNum <= head) must be ignored so a stale out-of-order
// block can never clobber a newer overlay cell (which would be served as
// "verified") or strand entries below the prune cursor.
func TestOverlay_IgnoresNonForwardApply(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(1)
	o.apply(100, psAcct(a, 500, 5)) // head = 100, A = 500
	o.apply(95, psAcct(a, 300, 4))  // older block — must be ignored

	raw, at, ok := o.account(a)
	if !ok {
		t.Fatal("account missing")
	}
	if at != 100 {
		t.Errorf("lastBlock = %d, want 100 (older apply must not overwrite)", at)
	}
	acc, _ := decodeAccount(raw)
	if acc.Balance.Uint64() != 500 {
		t.Errorf("balance = %d, want 500 (stale 300 must not clobber)", acc.Balance.Uint64())
	}
	// re-applying the same block is also a no-op (idempotent).
	o.apply(100, psAcct(a, 999, 9))
	_, at2, _ := o.account(a)
	if at2 != 100 {
		t.Errorf("re-apply of head changed cell: lastBlock=%d", at2)
	}
	if _, ok := o.touched[95]; ok {
		t.Error("ignored block 95 must not leave a touched entry (leak)")
	}
}

// guard against accidental balance overflow handling in decode.
func TestOverlay_BigBalance(t *testing.T) {
	o := newMobileOverlay(900)
	a := addrOf(6)
	big := new(big.Int).Lsh(big.NewInt(1), 200) // 2^200 wei
	acc := account.NewAccount()
	acc.Balance.SetFromBig(big)
	acc.Nonce = 7
	o.apply(3, &ethel.PostState{
		Accounts: map[types.Address][]byte{a: acc.MarshalV2()},
		Storage:  map[types.Address]map[types.Hash][]byte{},
		Wiped:    map[types.Address]struct{}{},
	})
	raw, _, ok := o.account(a)
	if !ok {
		t.Fatal("miss")
	}
	got, _ := decodeAccount(raw)
	if got.Balance.ToBig().Cmp(big) != 0 {
		t.Errorf("big balance roundtrip failed: got %s want %s", got.Balance.String(), big.String())
	}
}
