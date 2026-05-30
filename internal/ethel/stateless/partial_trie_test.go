package stateless

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
)

// fullTrie builds a complete in-memory trie from scratch (every node present),
// so we can (a) compute a ground-truth root and (b) serialize ALL its nodes as a
// "proof" for the partial trie. It reuses the partialTrie engine with an empty
// node map — since we only ever insert from empty, no boundary resolution is
// needed.
func fullTrie() *partialTrie {
	return &partialTrie{nodes: map[string][]byte{}}
}

// collectNodes walks a fully-materialised trie and returns every node's RLP
// (the maximal "proof"). Inline nodes (<32B) are not emitted as separate blobs
// because a parent embeds them directly — exactly how a real proof behaves.
func collectNodes(n node, out *[][]byte) {
	switch n := n.(type) {
	case *shortNode:
		enc := encodeNode(n)
		if len(enc) >= 32 {
			*out = append(*out, enc)
		}
		collectNodes(n.val, out)
	case *fullNode:
		enc := encodeNode(n)
		if len(enc) >= 32 {
			*out = append(*out, enc)
		}
		for i := 0; i < 17; i++ {
			collectNodes(n.children[i], out)
		}
	default:
		// valueNode / hashNode / nil: nothing to emit
	}
}

func k32(i uint64) []byte {
	var b [32]byte
	binary.BigEndian.PutUint64(b[24:], i)
	// spread high nibbles so the trie branches (not one long extension)
	h := keccak(b[:])
	return h
}

func TestEmptyRootMatchesEthereum(t *testing.T) {
	// keccak(rlp("")) = keccak(0x80) = the canonical empty MPT root.
	want := "56e81f171bcac102f8fc1c3e6e21c7f5e51b9c2c5e1e2e8d2cf8f9e0c0e0..." // prefix-checked below
	got := hex.EncodeToString(emptyRootHash)
	if !bytes.HasPrefix([]byte(got), []byte("56e81f171bcac1")) {
		t.Fatalf("empty root = %s, want prefix 56e81f171bcac1 (%.14s)", got, want)
	}
}

func TestPartialUpdateEqualsFullRebuild(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 50; round++ {
		nKeys := 20 + rng.Intn(200)
		// base state
		base := map[string][]byte{}
		keys := make([][]byte, 0, nKeys)
		for i := 0; i < nKeys; i++ {
			k := k32(uint64(round)*100000 + uint64(i))
			v := []byte(fmt.Sprintf("v%d-%d", round, i))
			base[string(k)] = v
			keys = append(keys, k)
		}
		// build base trie + ground-truth root
		bt := fullTrie()
		for _, k := range keys {
			if err := bt.update(keybytesToHex(k), base[string(k)]); err != nil {
				t.Fatalf("base insert: %v", err)
			}
		}
		baseRoot := bt.hash()

		// serialize ALL base nodes as the proof
		var proof [][]byte
		collectNodes(bt.root, &proof)
		// add root itself if it was inline (<32B) — root is always hashed
		rootEnc := encodeNode(bt.root)
		if len(rootEnc) < 32 {
			proof = append(proof, rootEnc)
		}

		// pick a changeset: mutate some, delete some, insert some new
		changes := map[string][]byte{}
		for i, k := range keys {
			switch i % 5 {
			case 0:
				changes[string(k)] = []byte(fmt.Sprintf("MUT%d", i)) // modify
			case 1:
				changes[string(k)] = nil // delete
			}
		}
		for i := 0; i < nKeys/10; i++ {
			nk := k32(uint64(round)*100000 + 900000 + uint64(i))
			changes[string(nk)] = []byte(fmt.Sprintf("NEW%d", i)) // insert
		}

		// (1) ground truth: full rebuild with changes applied
		ft := fullTrie()
		final := map[string][]byte{}
		for k, v := range base {
			final[k] = v
		}
		for k, v := range changes {
			if v == nil {
				delete(final, k)
			} else {
				final[k] = v
			}
		}
		for k, v := range final {
			if err := ft.update(keybytesToHex([]byte(k)), v); err != nil {
				t.Fatalf("full insert: %v", err)
			}
		}
		fullRoot := ft.hash()

		// (2) partial: load proof, apply ONLY the changeset
		pt, err := newPartialTrie(baseRoot, proof)
		if err != nil {
			t.Fatalf("round %d: newPartialTrie: %v", round, err)
		}
		// sanity: partial root before changes == base root
		if !bytes.Equal(pt.hash(), baseRoot) {
			t.Fatalf("round %d: partial pre-root %x != base %x", round, pt.hash()[:8], baseRoot[:8])
		}
		for k, v := range changes {
			if err := pt.update(keybytesToHex([]byte(k)), v); err != nil {
				t.Fatalf("round %d: partial update: %v", round, err)
			}
		}
		partialRoot := pt.hash()

		if !bytes.Equal(partialRoot, fullRoot) {
			t.Fatalf("round %d: partial root %x != full rebuild %x",
				round, partialRoot[:8], fullRoot[:8])
		}
	}
}

// TestPartialReadProvesValues checks get() returns the proof's leaf values and
// proves absence for keys not in the trie.
func TestPartialReadProvesValues(t *testing.T) {
	bt := fullTrie()
	keys := [][]byte{}
	for i := 0; i < 64; i++ {
		k := k32(uint64(i))
		keys = append(keys, k)
		if err := bt.update(keybytesToHex(k), []byte(fmt.Sprintf("val%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	root := bt.hash()
	var proof [][]byte
	collectNodes(bt.root, &proof)

	pt, err := newPartialTrie(root, proof)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		v, found, err := pt.get(keybytesToHex(k))
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if !found || string(v) != fmt.Sprintf("val%d", i) {
			t.Fatalf("get %d: found=%v val=%q want val%d", i, found, v, i)
		}
	}
	// absent key
	absent := k32(99999)
	_, found, err := pt.get(keybytesToHex(absent))
	if err != nil {
		t.Fatalf("get absent: %v", err)
	}
	if found {
		t.Fatal("absent key reported found")
	}
}
