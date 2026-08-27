// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// TestObjectCacheFollowsLiveSet: the single-entry object cache must never
// return an object the live set no longer holds — after a createObject revert,
// after Reset, and after the object is replaced by a new one.
func TestObjectCacheFollowsLiveSet(t *testing.T) {
	addr := types.Address{0xc1}
	sdb := New(discardChangesReader{})

	// Populate the cache through a normal read.
	sdb.SetNonce(addr, 3)
	first := sdb.getStateObject(addr)
	if sdb.lastObj != first || sdb.lastAddr != addr {
		t.Fatal("cache not primed by the live lookup")
	}

	// A snapshot, a fresh object at the same address, then revert: the
	// object created inside the snapshot must not survive in the cache.
	snap := sdb.Snapshot()
	sdb.CreateAccount(addr, true)
	created := sdb.getStateObject(addr)
	if created == first {
		t.Fatal("CreateAccount did not replace the object")
	}
	sdb.RevertToSnapshot(snap)
	after := sdb.getStateObject(addr)
	if after == created {
		t.Fatal("cache returned the reverted object")
	}
	if after.Nonce() != 3 {
		t.Fatalf("nonce after revert = %d, want 3", after.Nonce())
	}

	// Reset must forget the cache entirely.
	sdb.Reset()
	if sdb.lastObj != nil {
		t.Fatal("Reset left an object in the cache")
	}
	sdb.SetBalance(addr, uint256.NewInt(5))
	if got := sdb.GetBalance(addr); got.Uint64() != 5 {
		t.Fatalf("balance after Reset = %d, want 5", got.Uint64())
	}
}

// TestObjectCacheDoesNotCrossAddresses: a hit on one address must not be
// returned for another.
func TestObjectCacheDoesNotCrossAddresses(t *testing.T) {
	a, b := types.Address{0xa}, types.Address{0xb}
	sdb := New(discardChangesReader{})
	sdb.SetNonce(a, 1)
	sdb.SetNonce(b, 2)
	if sdb.getStateObject(a).Nonce() != 1 || sdb.getStateObject(b).Nonce() != 2 || sdb.getStateObject(a).Nonce() != 1 {
		t.Fatal("cache confused two addresses")
	}
}
