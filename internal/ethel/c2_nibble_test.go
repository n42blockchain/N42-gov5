// C2 Phase-1 de-risk: prove that splitting the hashed leaf set by top account
// nibble into 16 independent subtrie builds (CalcTrieRootStreamingCutoff cutoff=1)
// and recombining (CombineNibbleSubtries) yields the SAME state root as the serial
// full build. If this holds byte-exact, the parallel-subtrie root build is viable.
package ethel

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

func sliceLeafIter(items [][2][]byte) trie.LeafIter {
	i := 0
	return func() ([]byte, []byte, bool, error) {
		if i >= len(items) {
			return nil, nil, false, nil
		}
		it := items[i]
		i++
		return it[0], it[1], true, nil
	}
}

// runC2Equivalence pins that StreamingFullStateRootC2 (parallel hash + 16-way
// parallel subtrie build + CombineNibbleSubtries) computes the same root as the
// serial MDBX-load + FlatDBTrieLoader path.
func runC2Equivalence(t *testing.T, seed func(*testing.T, kv.RwTx), workers int) {
	ctx := context.Background()
	refDB := memdb.New(t.TempDir())
	defer refDB.Close()
	var refRoot types.Hash
	{
		tx, err := refDB.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, tx)
		r, err := FullStateRootVerify(nil, tx, 1)
		if err != nil {
			t.Fatalf("FullStateRootVerify: %v", err)
		}
		refRoot = r
		tx.Rollback()
	}
	c2DB := memdb.New(t.TempDir())
	defer c2DB.Close()
	{
		tx, err := c2DB.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	root, err := StreamingFullStateRootC2(ctx, c2DB, workers, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("StreamingFullStateRootC2 (workers=%d): %v", workers, err)
	}
	if root != refRoot {
		t.Errorf("C2 parallel-subtrie root != serial root:\n  serial = %s\n  C2     = %s", refRoot.Hex(), root.Hex())
	}
}

func TestStreamingFullStateRootC2MatchesSerial(t *testing.T)     { runC2Equivalence(t, seedPlainStateLarge, 16) }
func TestStreamingFullStateRootC2MatchesSerial_4w(t *testing.T)  { runC2Equivalence(t, seedPlainStateLarge, 4) }
func TestStreamingFullStateRootC2MatchesSerial_1w(t *testing.T)  { runC2Equivalence(t, seedPlainStateLarge, 1) }
func TestStreamingFullStateRootC2MatchesSerial_Sm(t *testing.T)  { runC2Equivalence(t, seedPlainState, 8) }

func TestC2NibbleShardCombineMatchesSerial(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedPlainStateLarge(t, tx)

	// Reference: serial full root.
	refRoot, err := FullStateRootVerify(nil, tx, 1)
	if err != nil {
		t.Fatalf("FullStateRootVerify: %v", err)
	}

	// Build globally-sorted hashed leaf sets straight from PlainState.
	emptyCH := crypto.Keccak256Hash(nil)
	var accs [][2][]byte
	ac, _ := tx.Cursor(modules.Account)
	for k, v, e := ac.First(); k != nil; k, v, e = ac.Next() {
		if e != nil {
			t.Fatal(e)
		}
		if len(k) != 20 {
			continue
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			t.Fatal(err)
		}
		if a.CodeHash == (types.Hash{}) {
			a.CodeHash = emptyCH
		}
		accs = append(accs, [2][]byte{crypto.Keccak256(k), a.MarshalV2()})
	}
	ac.Close()
	sort.Slice(accs, func(i, j int) bool { return bytes.Compare(accs[i][0], accs[j][0]) < 0 })

	var stos [][2][]byte
	sc, _ := tx.Cursor(modules.Storage)
	for k, v, e := sc.First(); k != nil; k, v, e = sc.Next() {
		if e != nil {
			t.Fatal(e)
		}
		if len(k) < 52 {
			continue
		}
		comp := append(crypto.Keccak256(k[:20]), crypto.Keccak256(k[20:52])...)
		stos = append(stos, [2][]byte{comp, append([]byte(nil), v...)})
	}
	sc.Close()
	sort.Slice(stos, func(i, j int) bool { return bytes.Compare(stos[i][0], stos[j][0]) < 0 })

	// Shard by top nibble of the hashed account key; compute each subtrie hash.
	var subs [16][]byte
	for nib := 0; nib < 16; nib++ {
		var accI, stoI [][2][]byte
		for _, a := range accs {
			if int(a[0][0]>>4) == nib {
				accI = append(accI, a)
			}
		}
		if len(accI) == 0 {
			continue // absent nibble
		}
		for _, s := range stos {
			if int(s[0][0]>>4) == nib {
				stoI = append(stoI, s)
			}
		}
		loader := trie.NewFlatDBTrieLoader("c2", trie.NewRetainList(0), nil, nil, false)
		h, err := loader.CalcTrieRootStreamingCutoff(sliceLeafIter(accI), sliceLeafIter(stoI), 1)
		if err != nil {
			t.Fatalf("nibble %d subtrie: %v", nib, err)
		}
		hb := make([]byte, 32)
		copy(hb, h[:])
		subs[nib] = hb
	}

	root, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}
	if root != refRoot {
		t.Errorf("C2 nibble-shard root != serial root:\n  serial   = %s\n  combined = %s", refRoot.Hex(), root.Hex())
	}
}
