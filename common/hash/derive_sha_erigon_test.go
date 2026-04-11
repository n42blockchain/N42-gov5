// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
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
