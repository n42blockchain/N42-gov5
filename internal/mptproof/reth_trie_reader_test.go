package mptproof

import (
	"encoding/hex"
	"math/bits"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestRethTrieReader_AccountsRoot reads reth's AccountsTrie at the
// empty prefix and prints the root hash + first-level mask shape.
// Validates that DecodeRethBranchNodeCompact handles real reth
// data correctly.
func TestRethTrieReader_AccountsRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	r := NewRethTrieReader(src)

	// Reth stores branches starting at depth 1, not depth 0 — the
	// "root branch" is implicit (composed of the 16 depth-1 rows).
	// Query at nibble [0] which has all 16 children per probe.
	bn, ok, err := r.AccountBranchAt([]byte{0})
	if err != nil {
		t.Fatalf("AccountBranchAt([0]): %v", err)
	}
	if !ok {
		t.Fatalf("no AccountsTrie branch at nibble [0]")
	}
	t.Logf("AccountsTrie root branch:")
	t.Logf("  state_mask = 0x%04x (popcount %d)", bn.HasState, bits.OnesCount16(bn.HasState))
	t.Logf("  tree_mask  = 0x%04x", bn.HasTree)
	t.Logf("  hash_mask  = 0x%04x", bn.HasHash)
	t.Logf("  has_root   = %v", bn.HasRoot)
	if bn.HasRoot {
		t.Logf("  root_hash  = 0x%x", bn.RootHash[:])
	}
	t.Logf("  child_hashes = %d entries", len(bn.Hashes))
	if len(bn.Hashes) > 0 {
		t.Logf("  first hash = 0x%x", bn.Hashes[0][:])
	}
}

// TestRethTrieReader_USDCStorageSubtreeRoot walks reth's StoragesTrie
// for USDC at empty subkey — that's the contract's storage-trie
// root branch. This is the key proof that per-contract sub-tries
// ARE available in reth's tables (unlike our HPH-unified scheme
// which collapsed them at depth 7).
func TestRethTrieReader_USDCStorageSubtreeRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)
	h := sha3.NewLegacyKeccak256()
	h.Write(usdc)
	var usdcHash [32]byte
	h.Sum(usdcHash[:0])
	t.Logf("USDC addrHash = 0x%x", usdcHash[:])

	r := NewRethTrieReader(src)
	// Try depth-1: USDC's storage subtree's first branch under
	// nibble 0 (most contracts have keccak space starting with 0
	// in some slots).
	bn, ok, err := r.StorageBranchAt(usdcHash[:], []byte{0})
	if err != nil {
		t.Fatalf("StorageBranchAt USDC [0]: %v", err)
	}
	if !ok {
		// Try walking to find ANY USDC storage row.
		t.Logf("USDC has no storage branch at [0] — probing other depths")
		for nib := 0; nib < 16; nib++ {
			b, ok, _ := r.StorageBranchAt(usdcHash[:], []byte{byte(nib)})
			if ok {
				bn = b
				t.Logf("USDC has storage branch at nibble [%x]: state=%04x tree=%04x hash=%04x has_root=%v",
					nib, b.HasState, b.HasTree, b.HasHash, b.HasRoot)
				break
			}
		}
		if bn == nil {
			t.Fatalf("USDC has NO storage branches in StoragesTrie (unexpected — should have lots)")
		}
		return
	}
	t.Logf("USDC storage-trie root branch:")
	t.Logf("  state_mask = 0x%04x (popcount %d)", bn.HasState, bits.OnesCount16(bn.HasState))
	t.Logf("  tree_mask  = 0x%04x", bn.HasTree)
	t.Logf("  hash_mask  = 0x%04x", bn.HasHash)
	t.Logf("  has_root   = %v", bn.HasRoot)
	if bn.HasRoot {
		t.Logf("  root_hash  = 0x%x", bn.RootHash[:])
	}
	t.Logf("  child_hashes = %d entries", len(bn.Hashes))
	if bits.OnesCount16(bn.HasState) < 8 {
		t.Errorf("USDC storage root has only %d children — expected dense (16 slots)",
			bits.OnesCount16(bn.HasState))
	}
}

