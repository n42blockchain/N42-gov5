// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/lib/rlphacks"
)

// TestUnitTrieMatchesFold drives random insert / overwrite / delete histories
// through the incremental trie and, after every batch, compares its root hash
// with the two from-scratch builders the reader uses: mptNodeRLP (the proof's
// subtree) and the GenStructStep fold. Short keys force inline (< 32 byte)
// nodes, extensions and branch collapses; long keys are the real shape.
//
// The fold is compared on the real shape only: with 4-6 nibble keys, where
// most nodes are embedded, it disagrees with mptNodeRLP (which embeds them as
// the yellow paper says). Tries keyed by 32-byte hashes never hold such nodes
// at a depth anything is recorded or folded at, so that regime is only
// checked against mptNodeRLP.
func TestUnitTrieMatchesFold(t *testing.T) {
	for _, tc := range []struct {
		nibbles, keySpace, rounds int
		maxVal                    int
		fold                      bool
	}{
		{nibbles: 4, keySpace: 40, rounds: 400, maxVal: 2},                 // tiny: inline nodes everywhere
		{nibbles: 6, keySpace: 300, rounds: 400, maxVal: 8},                // extensions + collapses
		{nibbles: 61, keySpace: 2000, rounds: 150, maxVal: 32, fold: true}, // storage unit below a 3-nibble prefix
	} {
		t.Run(fmt.Sprintf("nib%d", tc.nibbles), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(tc.nibbles)))
			// A fixed key universe so deletes and overwrites hit existing keys;
			// shared prefixes of random length make extensions likely.
			keys := make([][]byte, tc.keySpace)
			for i := range keys {
				k := make([]byte, tc.nibbles)
				for j := range k {
					k[j] = byte(rng.Intn(16))
				}
				if i > 0 && rng.Intn(3) == 0 {
					copy(k, keys[rng.Intn(i)][:rng.Intn(tc.nibbles)])
				}
				keys[i] = k
			}
			live := map[string][]byte{}
			var ut unitTrie
			for round := 0; round < tc.rounds; round++ {
				for op := 0; op < 1+rng.Intn(6); op++ {
					k := keys[rng.Intn(len(keys))]
					if rng.Intn(3) == 0 {
						_, had := live[string(k)]
						if ch := ut.update(k, nil); ch != had {
							t.Fatalf("round %d: delete changed=%v, key present=%v", round, ch, had)
						}
						delete(live, string(k))
						continue
					}
					v := make([]byte, 1+rng.Intn(tc.maxVal))
					rng.Read(v)
					v[0] |= 1 // stored values have no leading zero byte
					ut.update(k, rlpStr(rlpStr(v)))
					live[string(k)] = v
				}
				if ut.n != len(live) {
					t.Fatalf("round %d: trie has %d keys, want %d", round, ut.n, len(live))
				}
				got, ok := ut.rootHash()
				if len(live) == 0 {
					if ok {
						t.Fatalf("round %d: empty trie has a root", round)
					}
					continue
				}
				sorted := make([]string, 0, len(live))
				for k := range live {
					sorted = append(sorted, k)
				}
				sort.Strings(sorted)
				ml := make([]mleaf, len(sorted))
				fl := make([]foldLeaf, len(sorted))
				for i, k := range sorted {
					ml[i] = mleaf{nib: []byte(k), item: rlpStr(rlpStr(live[k]))}
					fl[i] = foldLeaf{remainder: append([]byte(k), 0x10), value: rlphacks.RlpSerializableBytes(live[k])}
				}
				if want := keccak(mptNodeRLP(ml, 0, nil, nil)); !ok || got != want {
					t.Fatalf("round %d (%d keys): incremental %x, mptNodeRLP %x", round, len(live), got[:6], want[:6])
				}
				if !tc.fold {
					continue
				}
				want, _, err := foldLeaves(fl)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("round %d (%d keys): incremental %x, fold %x", round, len(live), got[:6], want[:6])
				}
			}
		})
	}
}

// TestUnitTrieNoOpKeepsHash: rewriting a key with its current value, or
// deleting an absent key, reports no change — derive-ns emits a record only
// when a unit's hash moved.
func TestUnitTrieNoOpKeepsHash(t *testing.T) {
	var ut unitTrie
	k1, k2 := []byte{1, 2, 3, 4}, []byte{1, 2, 9, 9}
	ut.update(k1, rlpStr(rlpStr([]byte{7})))
	ut.update(k2, rlpStr(rlpStr([]byte{8})))
	h1, _ := ut.rootHash()
	if ut.update(k1, rlpStr(rlpStr([]byte{7}))) {
		t.Fatal("same value reported as a change")
	}
	if ut.update([]byte{5, 5, 5, 5}, nil) {
		t.Fatal("deleting an absent key reported as a change")
	}
	if h2, _ := ut.rootHash(); h1 != h2 {
		t.Fatal("hash moved on a no-op")
	}
}

// BenchmarkUnitTrieUpdate: one overwrite plus the root rehash in a 20k-key
// unit — what derive-ns pays per leaf-history row.
func BenchmarkUnitTrieUpdate(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	keys := make([][]byte, 20000)
	var ut unitTrie
	for i := range keys {
		k := make([]byte, 61)
		for j := range k {
			k[j] = byte(rng.Intn(16))
		}
		keys[i] = k
		ut.update(k, rlpStr(rlpStr([]byte{1, byte(i), byte(i >> 8)})))
	}
	ut.rootHash()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ut.update(keys[i%len(keys)], rlpStr(rlpStr([]byte{2, byte(i), byte(i >> 8), byte(i >> 16)})))
		ut.rootHash()
	}
}
