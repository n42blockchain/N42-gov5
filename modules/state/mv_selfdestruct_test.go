// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for SELFDESTRUCT poison-write semantics in EVMStateView.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func TestSelfdestruct_WipeShadowsBaseRead(t *testing.T) {
	// addr has slot=42 in the base store; tx 3 wipes addr; tx 7 reads
	// → must see 0, not 42.
	addr := mkAddr(0xa)
	slot := mkHash(0xb)

	mv := NewMVHashMap(16)
	r := newMVMockReader()
	r.storage[addr] = map[types.Hash][]byte{slot: uint256.NewInt(42).Bytes()}
	base := NewMVBaseFromStateReader(r)

	// Tx 3 wipes addr.
	wipeView := NewEVMStateView(NewMVStateView(mv, base, 3, 0))
	wipeView.WipeAddress(addr)
	wipeView.Inner().FlushWrites()

	// Tx 7 reads slot.
	readView := NewEVMStateView(NewMVStateView(mv, base, 7, 0))
	got, err := readView.ReadStorage(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("post-wipe read: got %s want 0", got.String())
	}
}

func TestSelfdestruct_WipeShadowsMVWrite(t *testing.T) {
	// tx 1 writes slot=10; tx 5 wipes; tx 9 reads → 0.
	addr := mkAddr(0xa)
	slot := mkHash(0xb)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	v1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	v1.WriteStorage(addr, slot, uint256.NewInt(10))
	v1.Inner().FlushWrites()

	v5 := NewEVMStateView(NewMVStateView(mv, base, 5, 0))
	v5.WipeAddress(addr)
	v5.Inner().FlushWrites()

	v9 := NewEVMStateView(NewMVStateView(mv, base, 9, 0))
	got, err := v9.ReadStorage(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("post-wipe read: got %s want 0 (wipe at tx 5 should shadow tx 1's write)", got.String())
	}
}

func TestSelfdestruct_PostWipeWriteSurvives(t *testing.T) {
	// tx 1 writes slot=10; tx 5 wipes; tx 7 writes slot=99; tx 9 reads → 99.
	addr := mkAddr(0xa)
	slot := mkHash(0xb)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	v1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	v1.WriteStorage(addr, slot, uint256.NewInt(10))
	v1.Inner().FlushWrites()

	v5 := NewEVMStateView(NewMVStateView(mv, base, 5, 0))
	v5.WipeAddress(addr)
	v5.Inner().FlushWrites()

	v7 := NewEVMStateView(NewMVStateView(mv, base, 7, 0))
	v7.WriteStorage(addr, slot, uint256.NewInt(99))
	v7.Inner().FlushWrites()

	v9 := NewEVMStateView(NewMVStateView(mv, base, 9, 0))
	got, err := v9.ReadStorage(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Uint64() != 99 {
		t.Errorf("post-wipe write read: got %s want 99 (tx 7's write should override tx 5's wipe)", got.String())
	}
}

func TestSelfdestruct_OtherAddrUnaffected(t *testing.T) {
	addrA := mkAddr(0xa)
	addrB := mkAddr(0xb)
	slot := mkHash(0x1)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	// addrA and addrB both have writes from tx 1.
	v1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	v1.WriteStorage(addrA, slot, uint256.NewInt(11))
	v1.WriteStorage(addrB, slot, uint256.NewInt(22))
	v1.Inner().FlushWrites()

	// tx 3 wipes addrA only.
	v3 := NewEVMStateView(NewMVStateView(mv, base, 3, 0))
	v3.WipeAddress(addrA)
	v3.Inner().FlushWrites()

	// tx 5 reads.
	v5 := NewEVMStateView(NewMVStateView(mv, base, 5, 0))
	gotA, _ := v5.ReadStorage(addrA, slot)
	gotB, _ := v5.ReadStorage(addrB, slot)
	if !gotA.IsZero() {
		t.Errorf("addrA: got %s want 0 (wiped)", gotA.String())
	}
	if gotB.Uint64() != 22 {
		t.Errorf("addrB: got %s want 22 (not wiped)", gotB.String())
	}
}

func TestSelfdestruct_IsAddressWiped(t *testing.T) {
	addr := mkAddr(0xa)
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	if NewEVMStateView(NewMVStateView(mv, base, 5, 0)).IsAddressWiped(addr) {
		t.Error("no wipe should be visible initially")
	}

	w := NewEVMStateView(NewMVStateView(mv, base, 2, 0))
	w.WipeAddress(addr)
	w.Inner().FlushWrites()

	if !NewEVMStateView(NewMVStateView(mv, base, 5, 0)).IsAddressWiped(addr) {
		t.Error("wipe at tx 2 should be visible to tx 5")
	}
	// tx 1 (before the wipe) doesn't see it.
	if NewEVMStateView(NewMVStateView(mv, base, 1, 0)).IsAddressWiped(addr) {
		t.Error("tx 1 should NOT see tx 2's wipe (later txIdx)")
	}
}

func TestSelfdestruct_CommitAppliesWipeFirst(t *testing.T) {
	// Mix wipe + post-wipe storage write on same addr; verify commit
	// applies wipe BEFORE storage so post-wipe slots survive.
	addr := mkAddr(0xa)
	slot1 := mkHash(0x1)
	slot2 := mkHash(0x2)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(map[string][]byte{
		// Pre-existing slot1 in base — must be wiped.
		string(EncodeStorageKey(addr, slot1)): {0xff},
	})

	// tx 0 wipes addr.
	v0 := NewEVMStateView(NewMVStateView(mv, base, 0, 0))
	v0.WipeAddress(addr)
	v0.Inner().FlushWrites()

	// tx 1 writes slot2 (post-wipe).
	v1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	v1.WriteStorage(addr, slot2, uint256.NewInt(0xaa))
	v1.Inner().FlushWrites()

	bc, err := FinalizeBlock(mv,
		[]TxOutput{
			{TxIdx: 0, GasUsed: 21000, Status: 1},
			{TxIdx: 1, GasUsed: 21000, Status: 1},
		},
		types.Address{})
	if err != nil {
		t.Fatal(err)
	}

	// Pre-load target with the base data so we can see the wipe clear it.
	target := NewMapApplyTarget()
	target.Storage[addr] = map[types.Hash][]byte{slot1: {0xff}}

	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	slots := target.Storage[addr]
	// slot1 should be GONE (wiped).
	if v, ok := slots[slot1]; ok {
		t.Errorf("slot1 should be wiped; got %x", v)
	}
	// slot2 should be PRESENT (post-wipe write).
	if v, ok := slots[slot2]; !ok {
		t.Errorf("slot2 missing")
	} else if len(v) != 1 || v[0] != 0xaa {
		t.Errorf("slot2 value: %x want 0xaa", v)
	}
}

func TestSelfdestruct_CommitFiltersPreWipeStorageWrite(t *testing.T) {
	// tx 1 writes slot_a = 111 (pre-wipe); tx 3 wipes addr; no post-wipe
	// write for slot_a. After FinalizeBlock + Apply the target must NOT
	// contain slot_a — the pre-wipe write must be shadowed by the wipe.
	addr := mkAddr(0xa)
	slotA := mkHash(0xaa)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	v1 := NewEVMStateView(NewMVStateView(mv, base, 1, 0))
	v1.WriteStorage(addr, slotA, uint256.NewInt(111))
	v1.Inner().FlushWrites()

	v3 := NewEVMStateView(NewMVStateView(mv, base, 3, 0))
	v3.WipeAddress(addr)
	v3.Inner().FlushWrites()

	bc, err := FinalizeBlock(mv,
		[]TxOutput{
			{TxIdx: 0, GasUsed: 21000, Status: 1},
			{TxIdx: 1, GasUsed: 21000, Status: 1},
			{TxIdx: 2, GasUsed: 21000, Status: 1},
			{TxIdx: 3, GasUsed: 21000, Status: 1},
		},
		types.Address{})
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate target as if base had an older slot_a value — the
	// Apply's WipeStorage phase should clear it, and the filtered
	// CommitEntry must NOT resurrect the tx-1 write.
	target := NewMapApplyTarget()
	target.Storage[addr] = map[types.Hash][]byte{slotA: {0x01}}

	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	if slots, ok := target.Storage[addr]; ok {
		if v, present := slots[slotA]; present {
			t.Errorf("slot_a should be wiped (not resurrected); got %x", v)
		}
	}
}

func TestSelfdestruct_KeyEncodingUnique(t *testing.T) {
	addr := mkAddr(0xa)
	slot := mkHash(0xb)
	codeHash := mkHash(0xc)

	keys := [][]byte{
		EncodeAccountKey(addr),
		EncodeStorageKey(addr, slot),
		EncodeCodeKey(codeHash),
		EncodeWipeKey(addr),
	}
	seen := make(map[string]int)
	for i, k := range keys {
		if prev, ok := seen[string(k)]; ok {
			t.Errorf("key[%d] collides with key[%d]", i, prev)
		}
		seen[string(k)] = i
	}
}
