// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// TestMerkleCollectApplyMatchesSync proves the async Merkle split
// (CollectTrieUpdates on a read snapshot + TrieUpdates.Apply) produces the
// SAME root and byte-identical TrieOf* tables as the synchronous incremental
// flush (flushTrieRootSerial via ComputeRoot), for the same delta on the same
// bootstrapped state.
func TestMerkleCollectApplyMatchesSync(t *testing.T) {
	base := mpCases[3] // medium-mixed: 256 accts, 64 with 8 slots each
	accts, stor, addrs := buildTestData(base)

	// Delta: balance changes, one slot change, one new account with storage,
	// one account deletion — the shapes that matter for TrieOf* updates.
	mkDelta := func() (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		dA := map[types.Address]*account.StateAccount{}
		dS := map[types.Address]map[types.Hash]*uint256.Int{}
		na := *accts[addrs[0]]
		na.Balance.SetUint64(31337)
		dA[addrs[0]] = &na
		var s types.Hash
		s[31] = 1
		nb := *accts[addrs[1]]
		dA[addrs[1]] = &nb
		dS[addrs[1]] = map[types.Hash]*uint256.Int{s: uint256.NewInt(987654)}
		var anew types.Address
		anew[0], anew[19] = 9, 42
		acctNew := &account.StateAccount{Initialised: true, Nonce: 3}
		acctNew.Balance.SetUint64(777)
		dA[anew] = acctNew
		dS[anew] = map[types.Hash]*uint256.Int{s: uint256.NewInt(555)}
		dA[addrs[4]] = nil // delete account with storage
		return dA, dS
	}

	bootstrap := func(t *testing.T) (kv.RwDB, kv.RwTx, *TrieRootComputer) {
		db := memdb.NewTestDB(t)
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(tx.Rollback)
		trc := NewTrieRootComputer()
		trc.SetRwTx(tx)
		trc.SetIncremental(false)
		if _, err := trc.ComputeRoot(accts, stor); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		return db, tx, trc
	}

	dumpTrie := func(t *testing.T, tx kv.Tx) map[string]string {
		out := map[string]string{}
		for _, table := range []string{modules.TrieOfAccounts, modules.TrieOfStorage} {
			c, err := tx.Cursor(table)
			if err != nil {
				t.Fatal(err)
			}
			for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
				if err != nil {
					t.Fatal(err)
				}
				out[table+"/"+string(k)] += string(v) + "|"
			}
			c.Close()
		}
		return out
	}

	// --- Reference: synchronous incremental flush. ---
	_, txSync, trcSync := bootstrap(t)
	dA, dS := mkDelta()
	trcSync.SetIncremental(true)
	rootSync, err := trcSync.ComputeRoot(dA, dS)
	if err != nil {
		t.Fatalf("sync incremental: %v", err)
	}
	trieSync := dumpTrie(t, txSync)

	// --- Async split: writeOnly exec, then Collect (read-only) + Apply. ---
	_, txAsync, trcAsync := bootstrap(t)
	dA2, dS2 := mkDelta()
	trcAsync.SetIncremental(true)
	trcAsync.SetWriteOnly(true) // staged exec: hashed state only, no TrieOf*
	if _, err := trcAsync.ComputeRoot(dA2, dS2); err != nil {
		t.Fatalf("writeOnly exec: %v", err)
	}

	// RetainList = the delta's touched keys, as BuildRetainListFromChangesets
	// would produce from the changesets of this window.
	rl := trie.NewRetainList(0)
	for addr := range dA2 {
		rl.AddKeyWithMarker(crypto.Keccak256(addr[:]), true)
	}
	for addr, slots := range dS2 {
		ah := crypto.Keccak256(addr[:])
		for slot := range slots {
			var comp [64]byte
			copy(comp[:32], ah)
			copy(comp[32:], crypto.Keccak256(slot[:]))
			rl.AddKeyWithMarker(comp[:], true)
		}
	}

	rootAsync, upd, err := CollectTrieUpdates(txAsync, rl)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if rootAsync != rootSync {
		t.Fatalf("root mismatch: sync %x async %x", rootSync[:], rootAsync[:])
	}
	if err := upd.Apply(txAsync); err != nil {
		t.Fatalf("apply: %v", err)
	}
	trieAsync := dumpTrie(t, txAsync)

	if len(trieSync) != len(trieAsync) {
		t.Fatalf("TrieOf* row count differs: sync %d async %d", len(trieSync), len(trieAsync))
	}
	for k, v := range trieSync {
		if trieAsync[k] != v {
			t.Fatalf("TrieOf* row differs at %x:\n sync  %x\n async %x", k, v, trieAsync[k])
		}
	}
}
