package trie_test

// g56: cross-checks the streaming trie-root entry points
// (CalcTrieRootStreaming / CalcTrieRootStreamingCutoff / CombineNibbleSubtries)
// against the cursor-driven CalcTrieRoot over the SAME account+storage set, plus
// WitnessRetainer (multi-key witness collection) and the dense/V2 branch marshal
// helpers in trie_root.go.

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

// g56Seed builds nAccts accounts, the first nWithStorage of which also get a
// handful of storage slots, all written into fresh HashedAccounts/HashedStorage
// tables. It returns the sorted account keys and a sorted list of (accKey,
// slotHash, value) storage leaves — exactly the two streams
// CalcTrieRootStreaming expects from a caller that doesn't want to hit the DB.
func g56Seed(t *testing.T, tx kv.RwTx, nAccts, nWithStorage int) (accKeys [][]byte, stoEntries [][2][]byte) {
	t.Helper()
	for i := 0; i < nAccts; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		k := crypto.Keccak256(seed[:])
		accKeys = append(accKeys, k)
		if err := tx.Put(kv.HashedAccounts, k, encAcct(uint64(i)+1, uint64(i)*1_000_000_007+13)); err != nil {
			t.Fatal(err)
		}
		if i < nWithStorage {
			for j := 0; j < 2+(i%4); j++ {
				var s [16]byte
				binary.BigEndian.PutUint64(s[8:], uint64(j)*97+1)
				slotHash := crypto.Keccak256(s[:])
				comp := append(append([]byte{}, k...), slotHash...)
				val := []byte{byte(i%251 + 1), byte(j + 1)}
				if err := tx.Put(kv.HashedStorage, comp, val); err != nil {
					t.Fatal(err)
				}
				stoEntries = append(stoEntries, [2][]byte{comp, val})
			}
		}
	}
	sort.Slice(accKeys, func(i, j int) bool { return string(accKeys[i]) < string(accKeys[j]) })
	sort.Slice(stoEntries, func(i, j int) bool { return string(stoEntries[i][0]) < string(stoEntries[j][0]) })
	return accKeys, stoEntries
}

// g56AccIter adapts a sorted slice of account keys into a trie.LeafIter,
// reading the value back out of HashedAccounts (so the test doesn't need to
// duplicate the storage-for-value encoding).
func g56AccIter(t *testing.T, tx kv.Tx, keys [][]byte) trie.LeafIter {
	i := 0
	return func() (key, value []byte, ok bool, err error) {
		if i >= len(keys) {
			return nil, nil, false, nil
		}
		k := keys[i]
		v, err := tx.GetOne(kv.HashedAccounts, k)
		if err != nil {
			return nil, nil, false, err
		}
		i++
		return k, v, true, nil
	}
}

func g56StoIter(entries [][2][]byte) trie.LeafIter {
	i := 0
	return func() (key, value []byte, ok bool, err error) {
		if i >= len(entries) {
			return nil, nil, false, nil
		}
		e := entries[i]
		i++
		return e[0], e[1], true, nil
	}
}

func TestCalcTrieRootStreaming_MatchesCalcTrieRoot_AccountsOnly(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	accKeys, _ := g56Seed(t, tx, 300, 0)

	fullLoader := trie.NewFlatDBTrieLoader("g56-full", trie.NewRetainList(0), nil, nil, false)
	wantRoot, err := fullLoader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	streamLoader := trie.NewFlatDBTrieLoader("g56-stream", trie.NewRetainList(0), nil, nil, false)
	gotRoot, err := streamLoader.CalcTrieRootStreaming(g56AccIter(t, tx, accKeys), g56StoIter(nil))
	if err != nil {
		t.Fatalf("CalcTrieRootStreaming: %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("streaming root mismatch: got %x want %x", gotRoot, wantRoot)
	}
}

func TestCalcTrieRootStreaming_MatchesCalcTrieRoot_WithStorage(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	accKeys, stoEntries := g56Seed(t, tx, 400, 120)

	fullLoader := trie.NewFlatDBTrieLoader("g56-full-sto", trie.NewRetainList(0), nil, nil, false)
	wantRoot, err := fullLoader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	streamLoader := trie.NewFlatDBTrieLoader("g56-stream-sto", trie.NewRetainList(0), nil, nil, false)
	gotRoot, err := streamLoader.CalcTrieRootStreaming(g56AccIter(t, tx, accKeys), g56StoIter(stoEntries))
	if err != nil {
		t.Fatalf("CalcTrieRootStreaming: %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("streaming root (with storage) mismatch: got %x want %x", gotRoot, wantRoot)
	}
}

// TestCalcTrieRootStreamingCutoff_CombineNibbleSubtries shards the same
// account set into its 16 top-nibble partitions, computes each shard's
// depth-1 subtrie hash via CalcTrieRootStreamingCutoff(…,1), and checks that
// folding them back together with CombineNibbleSubtries reproduces the full
// root CalcTrieRoot computes directly — the parallel-shard combine path.
func TestCalcTrieRootStreamingCutoff_CombineNibbleSubtries(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	accKeys, _ := g56Seed(t, tx, 500, 0)

	fullLoader := trie.NewFlatDBTrieLoader("g56-full-combine", trie.NewRetainList(0), nil, nil, false)
	wantRoot, err := fullLoader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	var shards [16][][]byte
	for _, k := range accKeys {
		nib := k[0] >> 4
		shards[nib] = append(shards[nib], k)
	}

	var subtrieHashes [16][]byte
	for nib := 0; nib < 16; nib++ {
		if len(shards[nib]) == 0 {
			continue
		}
		loader := trie.NewFlatDBTrieLoader("g56-shard", trie.NewRetainList(0), nil, nil, false)
		h, err := loader.CalcTrieRootStreamingCutoff(g56AccIter(t, tx, shards[nib]), g56StoIter(nil), 1)
		if err != nil {
			t.Fatalf("CalcTrieRootStreamingCutoff shard %d: %v", nib, err)
		}
		subtrieHashes[nib] = append([]byte{}, h[:]...)
	}

	gotRoot, err := trie.CombineNibbleSubtries(subtrieHashes)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("combined shard root mismatch: got %x want %x", gotRoot, wantRoot)
	}
}

// TestCombineNibbleSubtries_AllEmpty exercises the degenerate all-nibbles-absent
// path (every shard slice has len 0), which must reduce to the canonical empty
// root rather than panicking or looping.
func TestCombineNibbleSubtries_AllEmpty(t *testing.T) {
	var empty [16][]byte
	got, err := trie.CombineNibbleSubtries(empty)
	if err != nil {
		t.Fatalf("CombineNibbleSubtries: %v", err)
	}
	if got != trie.EmptyRoot {
		t.Errorf("all-empty combine = %x, want EmptyRoot %x", got, trie.EmptyRoot)
	}
}

// ---------------------------------------------------------------------
// WitnessRetainer
// ---------------------------------------------------------------------

func TestWitnessRetainer_CollectsNodesForRegisteredKeys(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	accKeys, _ := g56Seed(t, tx, 200, 0)

	rl := trie.NewRetainList(0)
	wr := trie.NewWitnessRetainer(rl)
	// Register a handful of scattered keys (every 17th account) as the witness
	// target set.
	var targets [][]byte
	for i := 0; i < len(accKeys); i += 17 {
		wr.AddHashedKey(accKeys[i])
		targets = append(targets, accKeys[i])
	}
	if len(targets) == 0 {
		t.Fatal("no targets selected")
	}

	loader := trie.NewFlatDBTrieLoader("g56-witness", rl, nil, nil, false)
	loader.SetWitnessRetainer(wr)
	if _, err := loader.CalcTrieRoot(tx, nil); err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	nodes := wr.Nodes()
	if len(nodes) == 0 {
		t.Fatal("WitnessRetainer.Nodes() returned no nodes for registered keys")
	}
	// De-duplication: re-reading Nodes() must be stable/idempotent.
	nodes2 := wr.Nodes()
	if len(nodes) != len(nodes2) {
		t.Errorf("Nodes() not stable across calls: %d vs %d", len(nodes), len(nodes2))
	}
}

// TestWitnessRetainerFromList_UsesExistingRetainList checks the
// NewWitnessRetainerFromList constructor: the keys already present in the
// RetainList (as added via AddKey through a prior ProofRetainer-less flow)
// become the relevance set without any further AddHashedKey calls.
func TestWitnessRetainerFromList_UsesExistingRetainList(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	accKeys, _ := g56Seed(t, tx, 100, 0)

	rl := trie.NewRetainList(0)
	rl.AddKey(accKeys[0])
	rl.AddKey(accKeys[5])

	wr := trie.NewWitnessRetainerFromList(rl)

	loader := trie.NewFlatDBTrieLoader("g56-witness-fromlist", rl, nil, nil, false)
	loader.SetWitnessRetainer(wr)
	if _, err := loader.CalcTrieRoot(tx, nil); err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	if len(wr.Nodes()) == 0 {
		t.Fatal("WitnessRetainerFromList collected no nodes")
	}
}

// ---------------------------------------------------------------------
// Dense / V2 branch marshal helpers
// ---------------------------------------------------------------------

// g56FakeSlot builds a synthetic hashStack-style slot: a single byte prefix
// followed by payload, stride-padded to 33 bytes as MarshalTrieNodeDense
// expects.
func g56FakeSlot(prefix byte, payload ...byte) [33]byte {
	var s [33]byte
	s[0] = prefix
	copy(s[1:], payload)
	return s
}

func TestMarshalUnmarshalTrieNodeDense_RoundTrip(t *testing.T) {
	// Two populated slots: digit 1 is a 32-byte hash ref (0xa0 prefix),
	// digit 3 is a short inline RLP list (0xc2 => 3-byte total).
	var hashPayload [32]byte
	for i := range hashPayload {
		hashPayload[i] = byte(i + 1)
	}
	slot1 := g56FakeSlot(0xa0, hashPayload[:]...)
	slot3 := g56FakeSlot(0xc2, 0x01, 0x02)

	slotData := make([]byte, 0, 66)
	slotData = append(slotData, slot1[:]...)
	slotData = append(slotData, slot3[:]...)

	stateMask := uint16(1<<1 | 1<<3)
	treeMask := uint16(1 << 1) // digit 1 is a deeper branch

	buf := trie.MarshalTrieNodeDense(stateMask, treeMask, slotData, nil)

	gotState, gotTree, slots, err := trie.UnmarshalTrieNodeDense(buf)
	if err != nil {
		t.Fatalf("UnmarshalTrieNodeDense: %v", err)
	}
	if gotState != stateMask || gotTree != treeMask {
		t.Fatalf("mask mismatch: state=%04x tree=%04x, want %04x/%04x", gotState, gotTree, stateMask, treeMask)
	}
	if slots[1] == nil || slots[1][0] != 0xa0 || len(slots[1]) != 33 {
		t.Errorf("slot1 round-trip wrong: %x", slots[1])
	}
	if slots[3] == nil || slots[3][0] != 0xc2 || len(slots[3]) != 3 {
		t.Errorf("slot3 round-trip wrong: %x", slots[3])
	}
	for d := 0; d < 16; d++ {
		if d != 1 && d != 3 && slots[d] != nil {
			t.Errorf("unexpected slot at digit %d: %x", d, slots[d])
		}
	}
}

func TestUnmarshalTrieNodeDense_TruncatedErrors(t *testing.T) {
	if _, _, _, err := trie.UnmarshalTrieNodeDense([]byte{0, 1}); err == nil {
		t.Error("expected error for buffer shorter than 4-byte header")
	}
	// Valid header claiming one slot (digit 0) but no payload bytes follow.
	hdr := make([]byte, 4)
	hdr[1] = 1 // state_mask = 0x0001
	if _, _, _, err := trie.UnmarshalTrieNodeDense(hdr); err == nil {
		t.Error("expected truncation error when a claimed slot has no bytes")
	}
	// Trailing garbage after a valid single empty-state parse must also error.
	trailing := append(hdr, 0xa0)
	trailing = append(trailing, make([]byte, 33)...) // overlong, triggers overrun or trailing-bytes error
	if _, _, _, err := trie.UnmarshalTrieNodeDense(trailing); err == nil {
		t.Error("expected an error for malformed/overlong dense buffer")
	}
}

func TestMarshalUnmarshalTrieNodeDenseV2_LeafMarkerAndHashRef(t *testing.T) {
	// digit 0: a leaf-hash slot (state set, tree clear, ext clear, prefix 0xa0)
	// -> must collapse to the 1-byte LeafMarker.
	// digit 2: an ext-hash slot (state set, ext set, prefix 0xa0) -> must stay
	// as a verbatim 33-byte hash ref.
	// digit 5: inline RLP (0xc3) -> stays verbatim.
	var leafHash, extHash [32]byte
	for i := range leafHash {
		leafHash[i] = byte(0x10 + i)
		extHash[i] = byte(0x20 + i)
	}
	slot0 := g56FakeSlot(0xa0, leafHash[:]...)
	slot2 := g56FakeSlot(0xa0, extHash[:]...)
	slot5 := g56FakeSlot(0xc3, 0x01, 0x02, 0x03)

	slotData := make([]byte, 0, 99)
	slotData = append(slotData, slot0[:]...)
	slotData = append(slotData, slot2[:]...)
	slotData = append(slotData, slot5[:]...)

	stateMask := uint16(1<<0 | 1<<2 | 1<<5)
	treeMask := uint16(0) // neither hash ref is flagged as "deeper branch" by the caller's tree bookkeeping
	extMask := uint16(1 << 2)

	buf := trie.MarshalTrieNodeDenseV2(stateMask, treeMask, extMask, slotData, nil)

	gotState, gotTree, slots, err := trie.UnmarshalTrieNodeDenseV2(buf)
	if err != nil {
		t.Fatalf("UnmarshalTrieNodeDenseV2: %v", err)
	}
	if gotState != stateMask {
		t.Fatalf("state mask mismatch: got %04x want %04x", gotState, stateMask)
	}
	// Reader infers tree bit from slot prefix == 0xa0; digit 2 kept its 0xa0
	// hash ref verbatim (it's an ext-hash, not leaf-markable), so the reader
	// sees it as "deeper branch" even though the writer's treeMask was 0 for it.
	if gotTree&(1<<2) == 0 {
		t.Errorf("expected inferred tree bit for digit 2 (verbatim hash ref), got %04x", gotTree)
	}
	if !trie.IsLeafMarker(slots[0]) {
		t.Errorf("digit 0 should have collapsed to LeafMarker, got %x", slots[0])
	}
	if trie.IsLeafMarker(slots[2]) || slots[2][0] != 0xa0 || len(slots[2]) != 33 {
		t.Errorf("digit 2 (ext hash) should stay verbatim 33B hash ref, got %x", slots[2])
	}
	if slots[5] == nil || slots[5][0] != 0xc3 || len(slots[5]) != 4 {
		t.Errorf("digit 5 inline slot round-trip wrong: %x", slots[5])
	}
}

func TestUnmarshalTrieNodeDenseV2_TooShortErrors(t *testing.T) {
	if _, _, _, err := trie.UnmarshalTrieNodeDenseV2([]byte{0}); err == nil {
		t.Error("expected error for buffer shorter than 2-byte header")
	}
}
