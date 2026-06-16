package trie_test

// P3 step 3: concurrency correctness. Run the 16 nibble shards on REAL concurrent
// goroutines, each with its OWN read transaction (the per-worker RoTx model a
// parallel build would use) and its OWN RetainList, then combine. Assert the root
// equals the serial CalcTrieRoot. Run with -race to prove no data races across
// the independent RoTx + loader instances. Offline (memdb).

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
)

func TestCalcTrieRootShardConcurrentMatchesSerial(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	// Seed + COMMIT so concurrent RoTx see a consistent snapshot.
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const nAccts, nWithStorage = 800, 120
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		k := crypto.Keccak256(seed[:])
		if err := wtx.Put(kv.HashedAccounts, k, encAcct(uint64(i)+1, uint64(i)*1_000_000_007+13)); err != nil {
			t.Fatal(err)
		}
		if i < nWithStorage {
			for j := 0; j < 2+(i%6); j++ {
				var s [16]byte
				binary.BigEndian.PutUint64(s[0:], uint64(i))
				binary.BigEndian.PutUint64(s[8:], uint64(j)*1099511628211+1)
				full := append(append([]byte{}, k...), crypto.Keccak256(s[:])...)
				if err := wtx.Put(kv.HashedStorage, full, u64be(uint64(i)*97+uint64(j)+1)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Serial reference (own RoTx).
	rtx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refRoot, err := trie.NewFlatDBTrieLoader("ref", trie.NewRetainList(0), nil, nil, false).CalcTrieRoot(rtx, nil)
	rtx.Rollback()
	if err != nil {
		t.Fatalf("serial: %v", err)
	}

	// 16 concurrent shards, each with its OWN RoTx + loader + RetainList.
	var subs [16][]byte
	var errs [16]error
	var wg sync.WaitGroup
	for nib := 0; nib < 16; nib++ {
		wg.Add(1)
		go func(nib int) {
			defer wg.Done()
			stx, e := db.BeginRo(ctx)
			if e != nil {
				errs[nib] = e
				return
			}
			defer stx.Rollback()
			l := trie.NewFlatDBTrieLoader("c", trie.NewRetainList(0), nil, nil, false)
			h, e := l.CalcTrieRootShard(stx, byte(nib), nil)
			if e != nil {
				errs[nib] = e
				return
			}
			if h != trie.EmptyRoot {
				hb := make([]byte, 32)
				copy(hb, h[:])
				subs[nib] = hb
			}
		}(nib)
	}
	wg.Wait()
	for nib, e := range errs {
		if e != nil {
			t.Fatalf("concurrent shard %x: %v", nib, e)
		}
	}

	combined, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		t.Fatalf("combine: %v", err)
	}
	if combined != refRoot {
		t.Fatalf("concurrent shard-combine root != serial:\n  serial   = %s\n  combined = %s", refRoot.Hex(), combined.Hex())
	}
	if refRoot == (types.Hash{}) || refRoot == trie.EmptyRoot {
		t.Fatal("degenerate root")
	}
	t.Logf("MATCH (concurrent, 16 RoTx): %s", refRoot.Hex())
}
