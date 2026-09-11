package trie_test

// P3 step 2: the REAL DATC per-window scenario — INCREMENTAL + STORAGE node
// collection. Round 0 builds TrieOfAccounts + TrieOfStorage caches; round 1
// changes some accounts and some storage slots; a dirty-path RetainList drives an
// incremental root. We assert the 16 shards' collected node updates (accounts AND
// storage) equal the serial loader's, modulo the depth-0 account root (which DATC
// does not persist). If clean, DATC's Datc*Node snapshots are byte-correct under
// the parallel path with NO collector-aware combine. Offline (memdb).

import (
	"context"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
)

type nodeMap struct {
	acc map[string][]byte
	sto map[string][]byte
}

func newNodeMap() *nodeMap { return &nodeMap{acc: map[string][]byte{}, sto: map[string][]byte{}} }

func (m *nodeMap) accColl() trie.HashCollector2 {
	return func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := string(append([]byte{}, keyHex...))
		if hasState == 0 {
			m.acc[k] = nil
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		m.acc[k] = append([]byte{}, trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)...)
		return nil
	}
}

func (m *nodeMap) stoColl() trie.StorageHashCollector2 {
	return func(accHash, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := string(append(append([]byte{}, accHash...), keyHex...))
		if hasState == 0 {
			m.sto[k] = nil
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		m.sto[k] = append([]byte{}, trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)...)
		return nil
	}
}

func TestCalcTrieRootShardIncrementalStorageNodeDiff(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	const nAccts, nWithStorage = 500, 80
	keys := make([][]byte, nAccts)
	type slot struct{ comp []byte } // addrHash||slotHash (64B)
	var slots []slot
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		k := crypto.Keccak256(seed[:])
		keys[i] = k
		if err := tx.Put(kv.HashedAccounts, k, encAcct(uint64(i)+1, uint64(i)*1_000_000_007+13)); err != nil {
			t.Fatal(err)
		}
		if i < nWithStorage {
			for j := 0; j < 2+(i%5); j++ {
				var s [16]byte
				binary.BigEndian.PutUint64(s[0:], uint64(i))
				binary.BigEndian.PutUint64(s[8:], uint64(j)*1099511628211+1)
				sh := crypto.Keccak256(s[:])
				full := append(append([]byte{}, k...), sh...)
				if err := tx.Put(kv.HashedStorage, full, u64be(uint64(i)*97+uint64(j)+1)); err != nil {
					t.Fatal(err)
				}
				slots = append(slots, slot{full})
			}
		}
	}

	// Round 0: build + persist TrieOfAccounts + TrieOfStorage caches.
	cache := newNodeMap()
	cl := trie.NewFlatDBTrieLoader("cache", trie.NewRetainList(0), cache.accColl(), cache.stoColl(), false)
	if _, err := cl.CalcTrieRoot(tx, nil); err != nil {
		t.Fatalf("build cache: %v", err)
	}
	for k, v := range cache.acc {
		if v == nil {
			_ = tx.Delete(kv.TrieOfAccounts, []byte(k))
			continue
		}
		if err := tx.Put(kv.TrieOfAccounts, []byte(k), v); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range cache.sto {
		if v == nil {
			continue
		}
		if err := tx.Put(kv.TrieOfStorage, []byte(k), v); err != nil {
			t.Fatal(err)
		}
	}

	// Round 1: change every 9th account, and a slice of storage slots.
	var changedKeys [][]byte
	for i := 0; i < nAccts; i += 9 {
		if err := tx.Put(kv.HashedAccounts, keys[i], encAcct(uint64(i)+5000, uint64(i)*4242+7)); err != nil {
			t.Fatal(err)
		}
		changedKeys = append(changedKeys, keys[i])
	}
	for si := 0; si < len(slots); si += 5 {
		comp := slots[si].comp
		if err := tx.Put(kv.HashedStorage, comp, u64be(uint64(si)*131+5)); err != nil {
			t.Fatal(err)
		}
		changedKeys = append(changedKeys, comp) // composite key retains the storage path
	}

	mkRetain := func() *trie.RetainList {
		rl := trie.NewRetainList(0)
		for _, k := range changedKeys {
			rl.AddKeyWithMarker(k, true)
		}
		return rl
	}

	// Serial incremental: capture node updates.
	serial := newNodeMap()
	sl := trie.NewFlatDBTrieLoader("ser", mkRetain(), serial.accColl(), serial.stoColl(), false)
	if _, err := sl.CalcTrieRoot(tx, nil); err != nil {
		t.Fatalf("serial incremental: %v", err)
	}

	// Parallel incremental: union of 16 shard collectors.
	par := newNodeMap()
	for nib := 0; nib < 16; nib++ {
		l := trie.NewFlatDBTrieLoader("sh", mkRetain(), par.accColl(), par.stoColl(), false)
		if _, err := l.CalcTrieRootShard(tx, byte(nib), nil); err != nil {
			t.Fatalf("shard %x: %v", nib, err)
		}
	}

	diff := func(name string, ser, par map[string][]byte, allowMissing func(string) bool) {
		var onlySer, onlyPar, mism []string
		for k, sv := range ser {
			pv, ok := par[k]
			if !ok {
				if allowMissing(k) {
					continue
				}
				onlySer = append(onlySer, k)
				continue
			}
			if string(sv) != string(pv) {
				mism = append(mism, k)
			}
		}
		for k := range par {
			if _, ok := ser[k]; !ok {
				onlyPar = append(onlyPar, k)
			}
		}
		sort.Strings(onlySer)
		sort.Strings(onlyPar)
		sort.Strings(mism)
		t.Logf("[%s] ser=%d par=%d  onlySer(disallowed)=%d onlyPar=%d mismatch=%d",
			name, len(ser), len(par), len(onlySer), len(onlyPar), len(mism))
		for i, k := range onlySer {
			if i >= 5 {
				break
			}
			t.Logf("  [%s] onlySer keyLen=%d key=%x", name, len([]byte(k)), []byte(k))
		}
		if len(onlySer) != 0 {
			t.Errorf("[%s] %d nodes only in serial (beyond allowed)", name, len(onlySer))
		}
		if len(onlyPar) != 0 {
			t.Errorf("[%s] %d nodes only in parallel", name, len(onlyPar))
		}
		if len(mism) != 0 {
			t.Errorf("[%s] %d byte-mismatched nodes", name, len(mism))
		}
	}

	// Account nodes: only the depth-0 root (keyHex "") may be missing from parallel.
	diff("acc", serial.acc, par.acc, func(k string) bool { return len([]byte(k)) == 0 })
	// Storage nodes: each storage trie is fully inside one account's shard, so
	// NOTHING may be missing (storage roots are emitted within-shard).
	diff("sto", serial.sto, par.sto, func(k string) bool { return false })
}
