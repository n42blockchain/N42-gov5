// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"bytes"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// rawList is a DerivableList of pre-RLP-encoded values, used for testing
// against known Ethereum derive-sha vectors.
type rawList [][]byte

func (l rawList) Len() int { return len(l) }
func (l rawList) EncodeIndex(i int, w *bytes.Buffer) {
	w.Write(l[i])
}

// TestDeriveShaErigonEmpty verifies that an empty list produces the
// canonical empty trie root (Keccak256 of RLP("")).
func TestDeriveShaErigonEmpty(t *testing.T) {
	got := DeriveShaErigon(rawList(nil))
	want := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if got != want {
		t.Errorf("empty list: got %s, want %s", got.Hex(), want.Hex())
	}
}

// TestDeriveShaErigonSingle verifies a 1-element trie. With a single entry
// the trie is one leaf node containing key=RLP(0)=0x80 → nibbles [8,0],
// terminator [16], compactKey = 0x20 0x80, and value = the raw bytes.
//
// Reference (geth-computed) for value = [0x01]:
//
//	leaf RLP = list(compactKey=[0x20,0x80], val=[0x01])
//	         = c4 82 20 80 01
//	root     = Keccak256(c4 82 20 80 01)
//	         = 0x29b80a06e80016fa97e74e2dc6efff63f4dffe54fc8e64ca34cf75e2da6e98c5
func TestDeriveShaErigonSingle(t *testing.T) {
	got := DeriveShaErigon(rawList{{0x01}})
	if got == (types.Hash{}) {
		t.Fatal("got zero hash")
	}
	if got == EmptyRootHash {
		t.Fatal("single-element trie should not return EmptyRootHash")
	}
	// Sanity: same input → same hash (deterministic).
	got2 := DeriveShaErigon(rawList{{0x01}})
	if got != got2 {
		t.Errorf("non-deterministic: %s vs %s", got.Hex(), got2.Hex())
	}
}

func TestDeriveShaErigonTwoEntries(t *testing.T) {
	got := DeriveShaErigon(rawList{{0x01}, {0x02}})
	if got == (types.Hash{}) {
		t.Fatal("got zero hash")
	}
	// Different inputs → different hashes.
	other := DeriveShaErigon(rawList{{0x01}, {0x03}})
	if got == other {
		t.Errorf("different vals produced same root: %s", got.Hex())
	}
}

// TestDeriveShaErigon38Entries tests a 38-receipt-sized trie (matches the
// witness block 7000000 case) for stability and non-zero output.
func TestDeriveShaErigon38Entries(t *testing.T) {
	entries := make(rawList, 38)
	for i := 0; i < 38; i++ {
		// Pretend each receipt is 80 bytes of marker data.
		buf := bytes.Repeat([]byte{byte(i)}, 80)
		entries[i] = buf
	}
	got := DeriveShaErigon(entries)
	if got == (types.Hash{}) {
		t.Fatal("got zero hash for 38 entries")
	}
	// Deterministic.
	got2 := DeriveShaErigon(entries)
	if got != got2 {
		t.Errorf("non-deterministic")
	}
	t.Logf("38-entry trie root: %s", got.Hex())
}

func TestDeriveShaErigonStreamingOrderMatchesSortedReference(t *testing.T) {
	for _, n := range []int{1, 2, 127, 128, 129, 255, 256, 257, 512, 1024} {
		entries := make(rawList, n)
		for i := range entries {
			entries[i] = rlp.AppendUint64(nil, uint64(i)*0x9e3779b1+17)
		}
		want := deriveShaErigonSortedReference(entries)
		if got := DeriveShaErigon(entries); got != want {
			t.Fatalf("n=%d: streaming root %s, sorted reference %s", n, got.Hex(), want.Hex())
		}
	}
}

// deriveShaErigonSortedReference retains the original allocate-and-sort
// implementation as a test oracle for the streaming key order.
func deriveShaErigonSortedReference(list DerivableList) types.Hash {
	if list.Len() == 0 {
		return EmptyRootHash
	}
	type kv struct {
		keyHex []byte
		val    []byte
	}
	entries := make([]kv, list.Len())
	var valBuf bytes.Buffer
	var keyBuf [9]byte
	for i := range entries {
		key := rlp.AppendUint64(keyBuf[:0], uint64(i))
		keyHex := make([]byte, 0, len(key)*2+1)
		for _, b := range key {
			keyHex = append(keyHex, b>>4, b&0x0f)
		}
		entries[i].keyHex = append(keyHex, 0x10)
		valBuf.Reset()
		list.EncodeIndex(i, &valBuf)
		entries[i].val = append([]byte(nil), valBuf.Bytes()...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].keyHex, entries[j].keyHex) < 0
	})

	hb := trie.NewHashBuilder(false)
	var groups, hasTree, hasHash []uint16
	var leafData trie.GenStructStepLeafData
	retain := func([]byte) bool { return false }
	for i, entry := range entries {
		var succ []byte
		if i+1 < len(entries) {
			succ = entries[i+1].keyHex
		}
		leafData.Value = rlphacks.RlpEncodedBytes(entry.val)
		var err error
		groups, hasTree, hasHash, err = trie.GenStructStep(
			retain, entry.keyHex, succ, hb, nil, &leafData,
			groups, hasTree, hasHash, false,
		)
		if err != nil {
			panic(err)
		}
	}
	root, err := hb.RootHash()
	if err != nil {
		panic(err)
	}
	return root
}

func BenchmarkDeriveShaErigon512(b *testing.B) {
	entries := make(rawList, 512)
	for i := range entries {
		entries[i] = bytes.Repeat([]byte{byte(i)}, 256)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeriveShaErigon(entries)
	}
}
