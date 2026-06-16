package trie_test

// P3 step 1 (empirical): capture the TrieOfAccounts node bytes emitted by the
// SERIAL CalcTrieRoot vs the 16 per-nibble CalcTrieRootShard collectors, and DIFF
// them. DATC persists these intermediate nodes as Datc*Node snapshots, so the
// parallel path must reproduce the SAME node set byte-for-byte — root equality
// (P1/P2) is necessary but not sufficient. This test reveals which nodes the
// cutoff=1 shards do NOT emit (expected: the depth-0 root and depth-1 nodes, which
// the shards return as the cutoff hash rather than emitting), so we know exactly
// what a collector-aware combine must produce. Offline (memdb).

import (
	"context"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
)

func encAcct(nonce, bal uint64) []byte {
	a := account.NewAccount()
	a.Nonce = nonce
	a.Balance = *uint256.NewInt(bal)
	buf := make([]byte, a.EncodingLengthForStorage())
	a.EncodeForStorage(buf)
	return buf
}

func TestCalcTrieRootShardNodeCollectionDiff(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	const nAccts = 600
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		k := crypto.Keccak256(seed[:])
		if err := tx.Put(kv.HashedAccounts, k, encAcct(uint64(i)+1, uint64(i)*1_000_000_007+13)); err != nil {
			t.Fatal(err)
		}
	}

	collectInto := func(m map[string][]byte) trie.HashCollector2 {
		return func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
			k := string(append([]byte{}, keyHex...))
			if hasState == 0 {
				m[k] = nil // tombstone
				return nil
			}
			buf := make([]byte, len(hashes)+len(rootHash)+6)
			v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
			m[k] = append([]byte{}, v...)
			return nil
		}
	}

	// Serial: all nodes.
	serial := map[string][]byte{}
	sl := trie.NewFlatDBTrieLoader("ser", trie.NewRetainList(0), collectInto(serial), nil, false)
	if _, err := sl.CalcTrieRoot(tx, nil); err != nil {
		t.Fatalf("serial: %v", err)
	}

	// Parallel: union of 16 shard collectors.
	parallel := map[string][]byte{}
	for nib := 0; nib < 16; nib++ {
		l := trie.NewFlatDBTrieLoader("sh", trie.NewRetainList(0), collectInto(parallel), nil, false)
		if _, err := l.CalcTrieRootShard(tx, byte(nib), nil); err != nil {
			t.Fatalf("shard %x: %v", nib, err)
		}
	}

	// Diff.
	var onlySerial, onlyParallel, mismatch []string
	for k, sv := range serial {
		pv, ok := parallel[k]
		if !ok {
			onlySerial = append(onlySerial, k)
			continue
		}
		if string(sv) != string(pv) {
			mismatch = append(mismatch, k)
		}
	}
	for k := range parallel {
		if _, ok := serial[k]; !ok {
			onlyParallel = append(onlyParallel, k)
		}
	}
	sort.Strings(onlySerial)
	sort.Strings(onlyParallel)
	sort.Strings(mismatch)

	t.Logf("serial nodes=%d  parallel nodes=%d", len(serial), len(parallel))
	t.Logf("onlySerial=%d onlyParallel=%d mismatch=%d", len(onlySerial), len(onlyParallel), len(mismatch))
	show := func(label string, ks []string) {
		for i, k := range ks {
			if i >= 8 {
				t.Logf("  %s: … (%d more)", label, len(ks)-8)
				break
			}
			t.Logf("  %s: keyHexLen=%d keyHex=%x", label, len(k), []byte(k))
		}
	}
	show("onlySerial", onlySerial)
	show("onlyParallel", onlyParallel)
	show("mismatch", mismatch)

	// We EXPECT onlySerial to be exactly the nodes at depth 0 and 1 (the shards
	// return depth-1 as the cutoff hash, root is the combine's job). Anything
	// deeper missing, or any mismatch / extra, is a real bug to investigate.
	for _, k := range onlySerial {
		if len([]byte(k)) > 1 {
			t.Errorf("shard collectors missing a DEEP node (depth %d) keyHex=%x — not just the depth-0/1 combine layer", len([]byte(k)), []byte(k))
		}
	}
	if len(onlyParallel) != 0 {
		t.Errorf("parallel emitted %d nodes the serial did not (first few logged above)", len(onlyParallel))
	}
	if len(mismatch) != 0 {
		t.Errorf("%d nodes present in both but BYTE-DIFFERENT (first few logged above)", len(mismatch))
	}
}
