// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// epochReader serves a fixed storage value for every slot; accounts and code
// come from discardChangesReader's stub behaviour.
type epochReader struct {
	discardChangesReader
	value byte
}

func (r *epochReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return []byte{r.value}, nil
}

func u(v uint64) uint256.Int { return *uint256.NewInt(v) }

func getState(t *testing.T, sdb *IntraBlockState, addr types.Address, slot types.Hash) uint64 {
	t.Helper()
	var out uint256.Int
	sdb.GetState(addr, &slot, &out)
	return out.Uint64()
}

func getCommitted(t *testing.T, sdb *IntraBlockState, addr types.Address, slot types.Hash) uint64 {
	t.Helper()
	var out uint256.Int
	sdb.GetCommittedState(addr, &slot, &out)
	return out.Uint64()
}

// TestStorageEpochCommittedViewAcrossTransactions pins the semantics the
// single map must reproduce: within a transaction GetCommittedState returns
// the value at the transaction's start no matter how many writes happen;
// after FinalizeTx the last write IS the committed value; a revert of the
// first write in a transaction leaves the slot dirty at the pre-value with
// the committed view untouched.
func TestStorageEpochCommittedViewAcrossTransactions(t *testing.T) {
	addr := types.Address{0xaa}
	slot := types.Hash{1}
	sdb := New(&epochReader{value: 7})
	sdb.CreateAccount(addr, true)
	sdb.SetNonce(addr, 1) // keep the account non-empty through FinalizeTx
	if got := getCommitted(t, sdb, addr, slot); got != 0 {
		// created accounts start with empty storage
		t.Fatalf("committed on fresh created account = %d, want 0", got)
	}

	// tx 0: write twice; committed stays at the tx-start value (0), current follows.
	sdb.SetState(addr, &slot, u(10))
	sdb.SetState(addr, &slot, u(11))
	if got := getState(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx0 current = %d, want 11", got)
	}
	if got := getCommitted(t, sdb, addr, slot); got != 0 {
		t.Fatalf("tx0 committed = %d, want 0", got)
	}
	if err := sdb.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}

	// tx 1: the previous tx's last write is now the committed value.
	if got := getCommitted(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx1 committed = %d, want 11", got)
	}
	if got := getState(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx1 current = %d, want 11", got)
	}
	// A write, then a revert of that write: current returns to 11, committed still 11.
	snap := sdb.Snapshot()
	sdb.SetState(addr, &slot, u(12))
	if got := getCommitted(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx1 committed after write = %d, want 11", got)
	}
	sdb.RevertToSnapshot(snap)
	if got := getState(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx1 current after revert = %d, want 11", got)
	}
	if got := getCommitted(t, sdb, addr, slot); got != 11 {
		t.Fatalf("tx1 committed after revert = %d, want 11", got)
	}
	// The slot stays in the block-dirty set after the revert, as before.
	if slots := sdb.DirtyStorageSlots(addr); len(slots) != 1 || slots[0] != slot {
		t.Fatalf("dirty slots after revert = %v, want [%x]", slots, slot)
	}
	// Write again and finalize.
	sdb.SetState(addr, &slot, u(13))
	if err := sdb.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	if got := getCommitted(t, sdb, addr, slot); got != 13 {
		t.Fatalf("tx2 committed = %d, want 13", got)
	}
}

// TestStorageEpochReadThenWriteKeepsBlockOrigin: a slot read from the DB
// before being written keeps the DB value as both its committed view (until
// FinalizeTx) and its block-start value (for the writer's original).
func TestStorageEpochReadThenWriteKeepsBlockOrigin(t *testing.T) {
	addr := types.Address{0xbb}
	slot := types.Hash{2}
	sdb := New(&epochReader{value: 7})
	sdb.SetNonce(addr, 1)
	if got := getCommitted(t, sdb, addr, slot); got != 7 {
		t.Fatalf("db value = %d, want 7", got)
	}
	sdb.SetState(addr, &slot, u(9))
	if got := getCommitted(t, sdb, addr, slot); got != 7 {
		t.Fatalf("committed after write = %d, want 7", got)
	}
	if got := getState(t, sdb, addr, slot); got != 9 {
		t.Fatalf("current after write = %d, want 9", got)
	}
	obj := sdb.stateObjects[addr]
	if origin, ok := obj.blockOrigin(slot); !ok || origin.Uint64() != 7 {
		t.Fatalf("block origin = (%v,%v), want (7,true)", origin.Uint64(), ok)
	}
	w := new(discardChangesWriter)
	if err := sdb.FinalizeTx(&params.Rules{}, w); err != nil {
		t.Fatal(err)
	}
	if w.storageWrites != 1 || w.original.Uint64() != 7 || w.value.Uint64() != 9 {
		t.Fatalf("writer saw writes=%d original=%d value=%d", w.storageWrites, w.original.Uint64(), w.value.Uint64())
	}
	// tx 2: committed is now 9, block origin still 7.
	if got := getCommitted(t, sdb, addr, slot); got != 9 {
		t.Fatalf("tx2 committed = %d, want 9", got)
	}
	if origin, _ := obj.blockOrigin(slot); origin.Uint64() != 7 {
		t.Fatalf("block origin drifted to %d", origin.Uint64())
	}
	// A write that restores the DB value is still a write for the block.
	sdb.SetState(addr, &slot, u(7))
	if got := getState(t, sdb, addr, slot); got != 7 {
		t.Fatalf("current = %d, want 7", got)
	}
	if got := getCommitted(t, sdb, addr, slot); got != 9 {
		t.Fatalf("tx2 committed after write = %d, want 9", got)
	}
}

// TestStorageEpochResetsPerBlock: Reset must return the object's storage to a
// state where the DB is consulted again, and the epoch restarts.
func TestStorageEpochResetsPerBlock(t *testing.T) {
	addr := types.Address{0xcc}
	slot := types.Hash{3}
	reader := &epochReader{value: 5}
	sdb := New(reader)
	sdb.SetNonce(addr, 1)
	sdb.SetState(addr, &slot, u(1))
	if err := sdb.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	if sdb.storageEpoch != 2 {
		t.Fatalf("epoch after one FinalizeTx = %d, want 2", sdb.storageEpoch)
	}
	sdb.Reset()
	reader.value = 6
	if sdb.storageEpoch != 1 {
		t.Fatalf("epoch after Reset = %d, want 1", sdb.storageEpoch)
	}
	if got := getCommitted(t, sdb, addr, slot); got != 6 {
		t.Fatalf("after Reset the DB must be consulted: got %d, want 6", got)
	}
	if got := getState(t, sdb, addr, slot); got != 6 {
		t.Fatalf("after Reset current = %d, want 6", got)
	}
}
