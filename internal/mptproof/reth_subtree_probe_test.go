package mptproof

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestProbe_USDCDepth5BranchAt0000a: if walker says LandedOnLeaf at
// depth 5 nibble 'a' (with the deepest branch's tree-mask bit 10
// clear), then NO sub-tree should exist at prefix [0,0,0,0,a]. If
// a row DOES exist there, our walker's interpretation of the
// tree_mask is wrong (or the encoding differs from our assumption).
func TestProbe_USDCDepth5BranchAt0000a(t *testing.T) {
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

	// Walker landed at depth 5 prefix [0,0,0,0,a]. Probe whether a
	// sub-tree branch exists at this prefix.
	subtreePrefix := []byte{0, 0, 0, 0, 0xa}
	bn, ok, err := r.StorageBranchAt(usdcHash[:], subtreePrefix)
	if err != nil {
		t.Fatalf("StorageBranchAt: %v", err)
	}
	if !ok {
		t.Logf("NO sub-tree row at prefix [0,0,0,0,a] — consistent with leaf at this slot")
	} else {
		t.Logf("Sub-tree row FOUND at prefix [0,0,0,0,a]:")
		t.Logf("  state=0x%04x tree=0x%04x hash=0x%04x", bn.HasState, bn.HasTree, bn.HasHash)
		t.Logf("  → walker mis-classified! Should have descended, not landed.")
	}

	// Also dump the depth-4 parent (prefix [0,0,0,0]) for context.
	bn4, ok4, _ := r.StorageBranchAt(usdcHash[:], []byte{0, 0, 0, 0})
	if ok4 {
		t.Logf("PARENT branch at [0,0,0,0]: state=0x%04x tree=0x%04x hash=0x%04x",
			bn4.HasState, bn4.HasTree, bn4.HasHash)
		// Check slot 'a' (= nibble 10).
		hasState := bn4.HasState & (1 << 10)
		hasTree := bn4.HasTree & (1 << 10)
		hasHash := bn4.HasHash & (1 << 10)
		t.Logf("  slot 'a' (=10): state=%d tree=%d hash=%d", hasState>>10, hasTree>>10, hasHash>>10)
	}
}
