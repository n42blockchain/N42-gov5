package trie_test

// P1 prototype validation: prove that computing the state root as 16 INDEPENDENT
// top-nibble subtrie shards via the incremental cursor loader
// (FlatDBTrieLoader.CalcTrieRootShard, cutoff=1) and folding them with
// trie.CombineNibbleSubtries reproduces the serial CalcTrieRoot root BYTE-FOR-BYTE.
// This is the crux ("命门") of the parallel per-window DATC root design: if it
// holds, the shards can run concurrently (each on its own RoTx) for a large
// speedup. The test is fully offline (in-memory MDBX) and touches no production
// data, so it is safe to run alongside a live build.

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

// u64be returns the minimal big-endian encoding of v (leading zero bytes trimmed),
// matching how trimmed storage values are kept in HashedStorage.
func u64be(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return b[i:]
}

// seedHashedState writes a synthetic, keccak-distributed account set into
// HashedAccounts (+ a subset with storage into HashedStorage). Keys are
// keccak(counter) so the top nibbles spread across all 16 branches.
func seedHashedState(t *testing.T, tx kv.RwTx, nAccts, nWithStorage int) {
	t.Helper()
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		hashedAddr := crypto.Keccak256(seed[:]) // 32B

		a := account.NewAccount()
		a.Nonce = uint64(i) + 1
		a.Balance = *uint256.NewInt(uint64(i)*1_000_000_007 + 13)
		buf := make([]byte, a.EncodingLengthForStorage())
		a.EncodeForStorage(buf)
		if err := tx.Put(kv.HashedAccounts, hashedAddr, buf); err != nil {
			t.Fatal(err)
		}

		if i < nWithStorage {
			nSlots := 2 + (i % 5) // 2..6 slots
			for j := 0; j < nSlots; j++ {
				var s [16]byte
				binary.BigEndian.PutUint64(s[0:], uint64(i))
				binary.BigEndian.PutUint64(s[8:], uint64(j)*1099511628211+1)
				slotHash := crypto.Keccak256(s[:]) // 32B
				val := u64be(uint64(i)*97 + uint64(j) + 1)
				// HashedStorage is an auto-DupSort table: PUT the full logical key
				// addrHash(32)||slotHash(32)=64B with the value; the DupSort layer
				// stores physical key=addrHash(32), dup=slotHash(32)||value, which is
				// what CalcTrieRoot reads back via SeekBothRange(addrHash, …).
				fullKey := append(append([]byte{}, hashedAddr...), slotHash...)
				if err := tx.Put(kv.HashedStorage, fullKey, val); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestCalcTrieRootShardCombineMatchesSerial(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	seedHashedState(t, tx, 400, 60)

	// Reference: serial full root via the incremental cursor loader (TrieOf* empty
	// => full rebuild from HashedAccounts/HashedStorage).
	ref := trie.NewFlatDBTrieLoader("ref", trie.NewRetainList(0), nil, nil, false)
	refRoot, err := ref.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("serial CalcTrieRoot: %v", err)
	}
	if refRoot == trie.EmptyRoot {
		t.Fatal("serial root is EmptyRoot — seed produced no accounts")
	}

	// Parallel-shape: 16 independent top-nibble shards, each cutoff=1.
	var subs [16][]byte
	present := 0
	for nib := 0; nib < 16; nib++ {
		l := trie.NewFlatDBTrieLoader("shard", trie.NewRetainList(0), nil, nil, false)
		h, err := l.CalcTrieRootShard(tx, byte(nib), nil)
		if err != nil {
			t.Fatalf("CalcTrieRootShard nibble %x: %v", nib, err)
		}
		if h != trie.EmptyRoot {
			hb := make([]byte, 32)
			copy(hb, h[:])
			subs[nib] = hb
			present++
		}
	}
	if present == 0 {
		t.Fatal("no non-empty nibble shards")
	}
	t.Logf("non-empty nibble shards: %d/16", present)

	combined, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}

	if combined != refRoot {
		t.Fatalf("shard-combine root != serial root:\n  serial   = %s\n  combined = %s",
			refRoot.Hex(), combined.Hex())
	}
	t.Logf("MATCH: serial == shard-combine == %s", refRoot.Hex())
}

// TestCalcTrieRootShardAccountsOnly is the same check with NO storage — isolates
// the account-nibble partition + combine from any storage-trie effects.
func TestCalcTrieRootShardAccountsOnly(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	seedHashedState(t, tx, 300, 0)

	ref := trie.NewFlatDBTrieLoader("ref", trie.NewRetainList(0), nil, nil, false)
	refRoot, err := ref.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("serial CalcTrieRoot: %v", err)
	}

	var subs [16][]byte
	for nib := 0; nib < 16; nib++ {
		l := trie.NewFlatDBTrieLoader("shard", trie.NewRetainList(0), nil, nil, false)
		h, err := l.CalcTrieRootShard(tx, byte(nib), nil)
		if err != nil {
			t.Fatalf("CalcTrieRootShard nibble %x: %v", nib, err)
		}
		if h != (types.Hash{}) && h != trie.EmptyRoot {
			hb := make([]byte, 32)
			copy(hb, h[:])
			subs[nib] = hb
		}
	}
	combined, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}
	if combined != refRoot {
		t.Fatalf("accounts-only shard-combine root != serial root:\n  serial   = %s\n  combined = %s",
			refRoot.Hex(), combined.Hex())
	}
}
