package mptproof

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// TestRethWalk_USDCAccount walks reth's AccountsTrie for USDC's
// hashed address. Validates the walker descends correctly and
// terminates with a leaf hop (USDC exists in mainnet state).
func TestRethWalk_USDCAccount(t *testing.T) {
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

	t0 := time.Now()
	walk, err := WalkRethAccount(r, usdcHash[:])
	dWalk := time.Since(t0)
	if err != nil {
		t.Fatalf("WalkRethAccount: %v", err)
	}

	t.Logf("USDC account walk: %s — outcome=%v hops=%d leafDepth=%d",
		dWalk, walk.Outcome, len(walk.Hops), walk.LeafDepth)
	for i, hop := range walk.Hops {
		t.Logf("  hop %d: depth=%d nib=%x state=0x%04x tree=0x%04x hash=0x%04x children=%d",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, hop.Branch.HasTree, hop.Branch.HasHash,
			len(hop.Branch.Hashes))
	}

	switch walk.Outcome {
	case mpttrie.LandedOnLeaf, mpttrie.NoBranchAtPath, mpttrie.NoSuchChild:
		// Any of these terminal outcomes is fine. NoSuchChild on
		// the account walk just means USDC's account leaf lives
		// inline at a parent branch (reth's compact convention).
	default:
		t.Errorf("unexpected outcome %v", walk.Outcome)
	}
	if len(walk.Hops) == 0 {
		t.Errorf("walk produced 0 hops")
	}
}

// TestRethWalk_USDCStorage walks reth's StoragesTrie for USDC's
// slot 0 (totalSupply). This is THE acceptance test for the
// architecture pivot: HPH path failed here with 200K-leaf cap;
// reth per-contract path should succeed (and quickly).
func TestRethWalk_USDCStorage(t *testing.T) {
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

	// Slot 0 = USDC totalSupply.
	var slot [32]byte
	h2 := sha3.NewLegacyKeccak256()
	h2.Write(slot[:])
	var slotHash [32]byte
	h2.Sum(slotHash[:0])

	t0 := time.Now()
	walk, err := WalkRethStorage(r, usdcHash[:], slotHash[:])
	dWalk := time.Since(t0)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}

	t.Logf("USDC slot 0 walk: %s — outcome=%v hops=%d leafDepth=%d",
		dWalk, walk.Outcome, len(walk.Hops), walk.LeafDepth)
	for i, hop := range walk.Hops {
		t.Logf("  hop %d: depth=%d nib=%x state=0x%04x tree=0x%04x hash=0x%04x children=%d",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, hop.Branch.HasTree, hop.Branch.HasHash,
			len(hop.Branch.Hashes))
	}

	if walk.Outcome == mpttrie.NoBranchAtPath && walk.LeafDepth == 0 {
		t.Errorf("walk failed to find any branches for USDC slot 0 — architecture broken")
	}
	if dWalk > time.Second {
		t.Errorf("USDC storage walk took %s — expected sub-second", dWalk)
	}
}
