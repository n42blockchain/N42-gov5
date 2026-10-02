// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// g51NewTestIBS builds an IntraBlockState over an empty memdb view, handing
// the backing db to the caller so the view stays open for the test body.
func g51NewTestIBS(t *testing.T) (*IntraBlockState, kv.RwDB) {
	t.Helper()
	db := memdb.NewTestDB(t)
	var ibs *IntraBlockState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs = New(NewPlainStateReader(tx))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ibs, db
}

// TestG51PrepareAccessListAndQueries drives PrepareAccessList end to end:
// sender, destination, precompiles and an explicit tx access list, then
// checks AddressInAccessList / SlotInAccessList reflect every entry.
func TestG51PrepareAccessListAndQueries(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)

	sender := types.HexToAddress("0x00000000000000000000000000000000000000a1")
	dst := types.HexToAddress("0x00000000000000000000000000000000000000a2")
	precompile := types.HexToAddress("0x0000000000000000000000000000000000000001")
	extra := types.HexToAddress("0x00000000000000000000000000000000000000a3")
	slot := types.HexToHash("0x01")

	list := transaction.AccessList{
		{Address: extra, StorageKeys: []types.Hash{slot}},
	}

	ibs.PrepareAccessList(sender, &dst, []types.Address{precompile}, list)

	if !ibs.AddressInAccessList(sender) {
		t.Fatal("sender should be warm after PrepareAccessList")
	}
	if !ibs.AddressInAccessList(dst) {
		t.Fatal("dst should be warm after PrepareAccessList")
	}
	if !ibs.AddressInAccessList(precompile) {
		t.Fatal("precompile should be warm after PrepareAccessList")
	}
	addrPresent, slotPresent := ibs.SlotInAccessList(extra, slot)
	if !addrPresent || !slotPresent {
		t.Fatalf("extra access-list entry missing: addr=%v slot=%v", addrPresent, slotPresent)
	}

	unknown := types.HexToAddress("0x00000000000000000000000000000000000000ff")
	if ibs.AddressInAccessList(unknown) {
		t.Fatal("unrelated address must not be warm")
	}
}

// TestG51AccessListJournalRevert pins that both AddAddressToAccessList and
// AddSlotToAccessList push journal entries whose revert removes exactly what
// was added, leaving pre-existing entries untouched.
func TestG51AccessListJournalRevert(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)

	addr := types.HexToAddress("0x00000000000000000000000000000000000000b1")
	slot := types.HexToHash("0x02")

	snap := ibs.Snapshot()
	ibs.AddAddressToAccessList(addr)
	ibs.AddSlotToAccessList(addr, slot)

	if !ibs.AddressInAccessList(addr) {
		t.Fatal("address should be warm before revert")
	}
	if present, slotPresent := ibs.SlotInAccessList(addr, slot); !present || !slotPresent {
		t.Fatal("slot should be warm before revert")
	}

	ibs.RevertToSnapshot(snap)

	if ibs.AddressInAccessList(addr) {
		t.Fatal("address should be cold after revert")
	}
	if present, slotPresent := ibs.SlotInAccessList(addr, slot); present || slotPresent {
		t.Fatal("slot should be cold after revert")
	}
}

// TestG51AccessListSlotRevertKeepsAddress covers the partial-revert branch:
// adding a slot on an address that is already warm must push ONLY the slot
// journal entry, so reverting it clears the slot but leaves the address warm.
func TestG51AccessListSlotRevertKeepsAddress(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)

	addr := types.HexToAddress("0x00000000000000000000000000000000000000c1")
	slot := types.HexToHash("0x03")

	ibs.AddAddressToAccessList(addr)
	snap := ibs.Snapshot()
	ibs.AddSlotToAccessList(addr, slot)

	ibs.RevertToSnapshot(snap)

	if !ibs.AddressInAccessList(addr) {
		t.Fatal("address added before the snapshot must survive the revert")
	}
	if _, slotPresent := ibs.SlotInAccessList(addr, slot); slotPresent {
		t.Fatal("slot added after the snapshot must be gone")
	}
}

// TestG51GenerateRootHashEmpty exercises the early-return branch of
// GenerateRootHash when there are no dirty state objects.
func TestG51GenerateRootHashEmpty(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)

	if got := ibs.GenerateRootHash(); got != hash.NilHash {
		t.Fatalf("GenerateRootHash on a clean state = %x, want NilHash %x", got, hash.NilHash)
	}
}
