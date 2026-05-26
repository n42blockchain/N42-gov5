// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Repro / regression for TrieRootComputer incremental mode (SetIncremental
// true). eth-el's hashed-canonical path relies on incremental root updates;
// this test asserts the incremental root equals a full rebuild on the same
// post-modification state, across the scenarios a real block exercises.

package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// fullRoot computes the MPT root of the given full state via a fresh
// full-rebuild (incremental=false) in its own DB — the trusted reference.
func fullRoot(t *testing.T, accts map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int) types.Hash {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r, err := trc.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("fullRoot ComputeRoot: %v", err)
	}
	return r
}

// applyDelta returns a new full state = base with delta applied (account
// overwrite/delete; storage slot set/delete), mirroring how a block mutates
// state. base maps are not modified.
func applyDelta(
	baseA map[types.Address]*account.StateAccount,
	baseS map[types.Address]map[types.Hash]*uint256.Int,
	dA map[types.Address]*account.StateAccount,
	dS map[types.Address]map[types.Hash]*uint256.Int,
) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
	outA := make(map[types.Address]*account.StateAccount, len(baseA))
	for a, v := range baseA {
		outA[a] = v
	}
	for a, v := range dA {
		if v == nil {
			delete(outA, a)
		} else {
			outA[a] = v
		}
	}
	outS := make(map[types.Address]map[types.Hash]*uint256.Int, len(baseS))
	for a, slots := range baseS {
		m := make(map[types.Hash]*uint256.Int, len(slots))
		for s, v := range slots {
			m[s] = v
		}
		outS[a] = m
	}
	for a, slots := range dS {
		if outS[a] == nil {
			outS[a] = make(map[types.Hash]*uint256.Int, len(slots))
		}
		for s, v := range slots {
			if v == nil || v.IsZero() {
				delete(outS[a], s)
			} else {
				outS[a][s] = v
			}
		}
	}
	return outA, outS
}

func TestTrieRootIncrementalMatchesFull(t *testing.T) {
	base := mpCases[3] // medium-mixed: 256 accts, 64 with 8 slots each

	type deltaFn func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (
		map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int)

	cases := []struct {
		name string
		mk   deltaFn
	}{
		{"balance-only-on-storage-acct", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// THE suspected bug: dirty account whose storage is UNCHANGED.
			// addrs[0] has storage. Change only its balance.
			a0 := addrs[0]
			na := *accts[a0]
			na.Balance.SetUint64(999999)
			return map[types.Address]*account.StateAccount{a0: &na}, nil
		}},
		{"storage-slot-change", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// addrs[1] has storage; change one existing slot + mark acct dirty.
			a1 := addrs[1]
			var s types.Hash
			s[0] = 0
			s[31] = 0 // slot j=0 exists
			na := *accts[a1]
			return map[types.Address]*account.StateAccount{a1: &na},
				map[types.Address]map[types.Hash]*uint256.Int{a1: {s: uint256.NewInt(123456)}}
		}},
		{"new-account-no-storage", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			var anew types.Address
			anew[0] = 7
			anew[19] = 201
			nacct := &account.StateAccount{Initialised: true, Nonce: 7}
			nacct.Balance.SetUint64(555)
			return map[types.Address]*account.StateAccount{anew: nacct}, nil
		}},
		{"new-storage-slot", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// add a brand-new slot to an account that already has storage
			a2 := addrs[2]
			var s types.Hash
			s[0] = 9
			s[31] = 99 // not in buildTestData (j<8, s[0]=j%16)
			na := *accts[a2]
			return map[types.Address]*account.StateAccount{a2: &na},
				map[types.Address]map[types.Hash]*uint256.Int{a2: {s: uint256.NewInt(7777)}}
		}},
		{"storage-only-not-in-accts-map", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// change a slot but DO NOT put the account in the accounts map —
			// the account leaf must still be recomputed (storage root changed)
			// via the storage RetainList key's addrHash prefix.
			a3 := addrs[3]
			var s types.Hash
			s[0] = 0
			s[31] = 2 // slot j=2 exists
			return nil, map[types.Address]map[types.Hash]*uint256.Int{a3: {s: uint256.NewInt(888888)}}
		}},
		{"delete-account-with-storage", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// SELFDESTRUCT-style: account (with storage) deleted → nil.
			a4 := addrs[4]
			return map[types.Address]*account.StateAccount{a4: nil}, nil
		}},
		{"new-contract-with-storage", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			// CREATE-style: brand-new account WITH brand-new storage slots.
			var anew types.Address
			anew[0] = 3
			anew[1] = 77
			anew[19] = 222
			nacct := &account.StateAccount{Initialised: true, Nonce: 1}
			nacct.Balance.SetUint64(1234)
			nacct.CodeHash = types.BytesHash([]byte("newcontractcode"))
			slots := map[types.Hash]*uint256.Int{}
			for j := 0; j < 6; j++ {
				var s types.Hash
				s[0] = byte(j)
				s[31] = byte(j * 7)
				slots[s] = uint256.NewInt(uint64(j) + 1)
			}
			return map[types.Address]*account.StateAccount{anew: nacct},
				map[types.Address]map[types.Hash]*uint256.Int{anew: slots}
		}},
		{"mixed", func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
			dA := map[types.Address]*account.StateAccount{}
			dS := map[types.Address]map[types.Hash]*uint256.Int{}
			// balance change on 5 storage accounts (unchanged storage)
			for i := 0; i < 5; i++ {
				a := addrs[i]
				na := *accts[a]
				na.Balance.SetUint64(uint64(1_000_000 + i))
				dA[a] = &na
			}
			// storage change on 3 of them
			for i := 10; i < 13; i++ {
				a := addrs[i]
				var s types.Hash
				s[0] = 0
				s[31] = 1 // slot j=1
				dS[a] = map[types.Hash]*uint256.Int{s: uint256.NewInt(uint64(424242 + i))}
				na := *accts[a]
				dA[a] = &na
			}
			return dA, dS
		}},
	}

	for _, tc := range cases {
		tc := tc
		// false = N42-native TrieOf* (with-root); true = reth-shape
		// (stripToRethShape). Both must match the full rebuild.
		for _, rethShape := range []bool{false, true} {
			rethShape := rethShape
			name := tc.name
			if rethShape {
				name += "/reth-shape"
			}
			t.Run(name, func(t *testing.T) {
				runIncrementalCase(t, base, tc.mk, rethShape)
			})
		}
	}
}

// TestTrieRootIncrementalNoRootRecord exercises the reth-shaped TrieOfStorage
// (no empty-path keylen-32 "account.root" records, no +1 own-hash on keylen-33+
// records — see stripToRethShape). The incremental FlatDBTrieLoader computes
// storage roots from the cached child records + HashedStorage leaves without
// those cached root records (reth's model).
//
// FIXED (2026-05-25): SeekToAccount's no-root path in lib/trie/trie_root.go
// (skipState=false + prev reset + per-account level-state reset between
// accounts) makes both single- and multi-account cases correct at any depth.
// erigon relied on the keylen-32 record's _unmarshal to implicitly reset the
// cursor between accounts; reth omits it, so the reset is now explicit. Broad
// coverage in TestNoRootSweep.
func TestTrieRootIncrementalNoRootRecord(t *testing.T) {
	mk := func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		a0 := addrs[0]
		na := *accts[a0]
		na.Balance.SetUint64(7777777)
		return map[types.Address]*account.StateAccount{a0: &na}, nil
	}
	t.Run("single-account", func(t *testing.T) {
		// 1 account, 24 slots (deep, multi-level sub-branches) — must match.
		runIncrementalCase(t, mpTestCase{name: "noroot-1", nAcct: 1, perAddr: 24, storeFor: 1}, mk, true)
	})
	t.Run("multi-account", func(t *testing.T) {
		runIncrementalCase(t, mpTestCase{name: "noroot-multi", nAcct: 8, perAddr: 24, storeFor: 8}, mk, true)
	})
}

// runIncrementalCase bootstraps the full state, optionally reshapes TrieOfStorage
// to reth's form (stripLen32 → stripToRethShape), applies the delta incrementally
// and asserts the incremental root matches a full rebuild reference in both cases.
func runIncrementalCase(t *testing.T, base mpTestCase,
	mk func(accts map[types.Address]*account.StateAccount, addrs []types.Address) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int),
	stripLen32 bool) {
	accts, stor, addrs := buildTestData(base)

	// Bootstrap: full rebuild populates HashedAccounts/HashedStorage
	// + TrieOf* cache, returns the pre-modification root.
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r0, err := trc.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if stripLen32 {
		// Reshape TrieStorage to reth's on-disk form (no keylen-32 account.root
		// records, no +1 own-hash on keylen-33+). See stripToRethShape.
		stripToRethShape(t, tx)
	}

	dA, dS := mk(accts, addrs)

	// Incremental update on the cached TrieOf*.
	trc.SetIncremental(true)
	rInc, err := trc.ComputeRoot(dA, dS)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}

	// Reference: full rebuild of (base + delta).
	refA, refS := applyDelta(accts, stor, dA, dS)
	rRef := fullRoot(t, refA, refS)

	if rInc != rRef {
		t.Fatalf("MISMATCH (stripLen32=%v)\n  incremental = %x\n  full        = %x\n  (pre-mod r0 = %x)", stripLen32, rInc[:], rRef[:], r0[:])
	}
	if rInc == r0 {
		t.Fatalf("incremental root unchanged from r0 %x — delta had no effect?", r0[:])
	}
}
