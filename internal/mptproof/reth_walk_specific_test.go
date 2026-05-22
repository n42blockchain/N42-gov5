package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// TestRB4_USDCSubtreeRebuild_Slot0000a010 is the canonical RB-4
// regression guard: pick a USDC storage slot whose walk reaches an
// inline (not separately rowed) sub-trie at depth 5, enumerate the
// 4 sibling slots, and verify expandSubtreeProofPath produces a
// sub-branch RLP whose keccak BYTE-EXACTLY matches the parent
// branch's stored child hash.
//
// If reth's StoragesTrie/HashedStorages diverge or if the sub-trie
// reconstruction encoding changes, this test fails loudly.
func TestRB4_USDCSubtreeRebuild_Slot0000a010(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, _ := NewRethHashedLeafSource(productionRethDB2k, 4096)
	defer src.Close()
	r := NewRethTrieReader(src)

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])

	// Known slot — walk's deepest hop will reference a 4-leaf inline
	// sub-trie at prefix [0,0,0,0,a,0].
	slotHashHex := "0000a01024dfe538ecd7033c75412089fea58e095d05b7b93fd3d07e0587d72f"
	slotHashBytes, _ := hex.DecodeString(slotHashHex)

	walk, err := WalkRethStorage(r, usdcHash[:], slotHashBytes)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}
	if walk.Outcome != mpttrie.LandedOnLeaf {
		t.Fatalf("expected LandedOnLeaf, got %v", walk.Outcome)
	}
	if len(walk.Hops) == 0 {
		t.Fatalf("walk produced 0 hops")
	}

	deepest := walk.Hops[len(walk.Hops)-1]
	if deepest.Branch.HasHash&(uint16(1)<<uint16(deepest.TargetNibble)) == 0 {
		t.Fatalf("deepest hop's target slot has no stored hash; cannot validate sub-trie")
	}
	expectedHash, _ := deepest.Branch.ChildHash(deepest.TargetNibble)

	slotNib := nibblesOf(slotHashBytes)
	subLeaves, lerr := enumerateUSDCStorageSubLeaves(src, usdcHash[:], slotNib[:walk.LeafDepth])
	if lerr != nil {
		t.Fatalf("enumerateUSDCStorageSubLeaves: %v", lerr)
	}
	if len(subLeaves) < 2 {
		t.Fatalf("expected multi-leaf inline subtree, got %d sub-leaves", len(subLeaves))
	}

	keyTail := append([]byte{}, slotNib[walk.LeafDepth:]...)
	keyTail = append(keyTail, 0x10)
	expanded, eerr := expandSubtreeProofPath(subLeaves, keyTail)
	if eerr != nil {
		t.Fatalf("expandSubtreeProofPath: %v", eerr)
	}
	if len(expanded) == 0 {
		t.Fatalf("expandSubtreeProofPath returned no nodes")
	}

	// The first expanded node is the sub-trie root; its keccak must
	// match the parent branch's stored child hash byte-for-byte.
	hsum := sha3.NewLegacyKeccak256()
	hsum.Write(expanded[0])
	var gotHash [32]byte
	hsum.Sum(gotHash[:0])
	if gotHash != expectedHash {
		t.Fatalf("sub-trie root keccak mismatch:\n  got      = %x\n  expected = %x\n  expanded[0] (%d B) = %x",
			gotHash[:], expectedHash[:], len(expanded[0]), expanded[0])
	}
	t.Logf("RB-4 OK: %d sub-leaves → sub-trie root keccak %x matches reth byte-exact",
		len(subLeaves), gotHash[:8])
}

// readValueAtSlotHash: small helper kept here as it's only used by
// this specific test for diagnostic reads.
func readValueAtSlotHash(t *testing.T, src *RethHashedLeafSource, addrHash, slotHash []byte) []byte {
	t.Helper()
	tx, _ := src.db.BeginRo(context.Background())
	defer tx.Rollback()
	c, _ := tx.CursorDupSort(rethHashedStoragesTable)
	defer c.Close()
	v, _ := c.SeekBothRange(addrHash, slotHash)
	if v == nil || len(v) < 32 {
		return nil
	}
	for i := 0; i < 32; i++ {
		if v[i] != slotHash[i] {
			return nil
		}
	}
	return append([]byte{}, v[32:]...)
}
