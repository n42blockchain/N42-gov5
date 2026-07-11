package trie_test

// P2 prototype validation: prove the INCREMENTAL-CACHE path of the nibble-shard
// design. Unlike P1 (empty TrieOf* => each shard full-rebuilds its nibble), here
// we first populate TrieOfAccounts (round 0), then change a subset of accounts
// (round 1) and drive a RetainList that retains ONLY the changed paths — so the
// loader reuses cached subtree hashes for everything unchanged. We assert that
// running 16 incremental shards (each with its OWN RetainList, as a real
// concurrent worker would) + CombineNibbleSubtries reproduces the serial
// incremental CalcTrieRoot BYTE-FOR-BYTE. This is the path a parallel per-window
// DATC root would actually take. Offline (memdb); touches no production data.

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
)

func acctEnc(nonce uint64, bal uint64) []byte {
	a := account.NewAccount()
	a.Nonce = nonce
	a.Balance = *uint256.NewInt(bal)
	buf := make([]byte, a.EncodingLengthForStorage())
	a.EncodeForStorage(buf)
	return buf
}

// buildAccountTrieCache runs a serial full-rebuild CalcTrieRoot with a collector
// that persists intermediate nodes into TrieOfAccounts, populating the cache the
// way TrieRootComputer's Phase 3/4 does. Returns the root.
func buildAccountTrieCache(t *testing.T, tx kv.RwTx) types.Hash {
	t.Helper()
	type kvp struct{ k, v []byte }
	var upd []kvp
	accColl := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append([]byte{}, keyHex...)
		if hasState == 0 {
			upd = append(upd, kvp{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		upd = append(upd, kvp{k, append([]byte{}, v...)})
		return nil
	}
	l := trie.NewFlatDBTrieLoader("cache", trie.NewRetainList(0), accColl, nil, false)
	root, err := l.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("build cache CalcTrieRoot: %v", err)
	}
	for _, u := range upd {
		if u.v == nil {
			_ = tx.Delete(kv.TrieOfAccounts, u.k)
			continue
		}
		if err := tx.Put(kv.TrieOfAccounts, u.k, u.v); err != nil {
			t.Fatalf("persist TrieOfAccounts: %v", err)
		}
	}
	return root
}

func TestCalcTrieRootShardIncrementalMatchesSerial(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Round 0: seed accounts and build the TrieOfAccounts cache.
	const nAccts = 500
	keys := make([][]byte, nAccts)
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		k := crypto.Keccak256(seed[:])
		keys[i] = k
		if err := tx.Put(kv.HashedAccounts, k, acctEnc(uint64(i)+1, uint64(i)*1_000_000_007+13)); err != nil {
			t.Fatal(err)
		}
	}
	r0 := buildAccountTrieCache(t, tx)

	// Round 1: change every 7th account's balance/nonce (spread across nibbles).
	var changed [][]byte
	for i := 0; i < nAccts; i += 7 {
		if err := tx.Put(kv.HashedAccounts, keys[i], acctEnc(uint64(i)+1000, uint64(i)*7777+99999)); err != nil {
			t.Fatal(err)
		}
		changed = append(changed, keys[i])
	}
	t.Logf("changed %d / %d accounts", len(changed), nAccts)

	mkRetain := func() *trie.RetainList {
		rl := trie.NewRetainList(0)
		for _, k := range changed {
			rl.AddKeyWithMarker(k, true)
		}
		return rl
	}

	// Reference: serial INCREMENTAL root (RetainList retains changed paths, cache
	// supplies the rest). No collectors — just compute the root.
	ref := trie.NewFlatDBTrieLoader("ref-inc", mkRetain(), nil, nil, false)
	r1, err := ref.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("serial incremental CalcTrieRoot: %v", err)
	}
	if r1 == r0 {
		t.Fatal("incremental root == round-0 root — changes did not take effect")
	}

	// Parallel-shape: 16 incremental shards, each with its OWN RetainList
	// (a real concurrent worker can't share the stateful lteIndex cursor).
	var subs [16][]byte
	for nib := 0; nib < 16; nib++ {
		l := trie.NewFlatDBTrieLoader("shard-inc", mkRetain(), nil, nil, false)
		h, err := l.CalcTrieRootShard(tx, byte(nib), nil)
		if err != nil {
			t.Fatalf("incremental shard nibble %x: %v", nib, err)
		}
		if h != trie.EmptyRoot {
			hb := make([]byte, 32)
			copy(hb, h[:])
			subs[nib] = hb
		}
	}
	combined, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}

	if combined != r1 {
		t.Fatalf("incremental shard-combine root != serial incremental root:\n  serial   = %s\n  combined = %s",
			r1.Hex(), combined.Hex())
	}
	t.Logf("MATCH (incremental): serial == shard-combine == %s", r1.Hex())
}
