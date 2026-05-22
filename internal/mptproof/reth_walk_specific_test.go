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

// TestRethWalk_USDCSlot0000a010: forced walk for the known slot
// whose [0,0,0,0,a] HAS a subtree row in reth (per the earlier
// probe). With the corrected tree_mask semantics (walker probes
// extended path), we should now DESCEND past depth 5, not land.
func TestRethWalk_USDCSlot0000a010(t *testing.T) {
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

	// Specific slot known to have a depth-5 subtree under USDC.
	slotHashHex := "0000a01024dfe538ecd7033c75412089fea58e095d05b7b93fd3d07e0587d72f"
	slotHashBytes, _ := hex.DecodeString(slotHashHex)

	walk, err := WalkRethStorage(r, usdcHash[:], slotHashBytes)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}
	t.Logf("walk: %d hops outcome=%v leafDepth=%d", len(walk.Hops), walk.Outcome, walk.LeafDepth)
	for i, hop := range walk.Hops {
		t.Logf("  hop %d: depth=%d nib=%x state=0x%04x tree=0x%04x hash=0x%04x",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, hop.Branch.HasTree, hop.Branch.HasHash)
	}
	if walk.Outcome != mpttrie.LandedOnLeaf {
		t.Logf("outcome != LandedOnLeaf, len(hops)=%d", len(walk.Hops))
	}

	// Look up slot's value from reth.
	reader := NewRethBackedReader(src)
	composite := append(append([]byte{}, usdc...), make([]byte, 32)...)
	// derive the actual slot bytes by trying simple known values
	// (only the slot HASH is what's in the trie — the original slot
	// can't be recovered cheaply). Skip and use raw 32-byte FF value
	// from value field directly.
	_ = composite
	_ = reader

	// Encode our hypothetical leaf for this slot and compare hash to
	// what reth recorded at the deepest hop's hash slot.
	if walk.Outcome == mpttrie.LandedOnLeaf && len(walk.Hops) > 0 {
		deepest := walk.Hops[len(walk.Hops)-1]
		if deepest.Branch.HasHash&(uint16(1)<<uint16(deepest.TargetNibble)) != 0 {
			expectedHash, _ := deepest.Branch.ChildHash(deepest.TargetNibble)
			t.Logf("expected leaf keccak (from depth-%d branch slot %x): %x",
				deepest.PrefixDepth, deepest.TargetNibble, expectedHash[:8])

			// Pull value from HashedStorages for this slot.
			val := readValueAtSlotHash(t, src, usdcHash[:], slotHashBytes)
			t.Logf("slot value: %x (%d B)", val, len(val))

			slotNib := nibblesOf(slotHashBytes)

			tryVariant := func(label string, rem []byte, value []byte, doubleWrap bool) {
				v := value
				if doubleWrap {
					v = encodeBytes(value)
				}
				rlpBytes := encodeLeafRLP(rem, v)
				h := sha3.NewLegacyKeccak256()
				h.Write(rlpBytes)
				var got [32]byte
				h.Sum(got[:0])
				ok := got == expectedHash
				t.Logf("  [%s] keccak=%x match=%v (leaf_len=%d)", label, got[:8], ok, len(rlpBytes))
			}

			remTerm := append(append([]byte{}, slotNib[walk.LeafDepth:]...), 0x10)
			tryVariant("term-strip-single", remTerm, stripLeadingZeros(val), false)

			// RB-4 subtree reconstruction: enumerate all leaves under
			// slotNib[:walk.LeafDepth] and run expandSubtreeProofPath.
			subLeaves, lerr := enumerateUSDCStorageSubLeaves(src, usdcHash[:], slotNib[:walk.LeafDepth])
			if lerr != nil {
				t.Fatalf("enumerateUSDCStorageSubLeaves: %v", lerr)
			}
			t.Logf("subLeaves count under prefix %v: %d", slotNib[:walk.LeafDepth], len(subLeaves))
			keyTail := append([]byte{}, slotNib[walk.LeafDepth:]...)
			keyTail = append(keyTail, 0x10)
			expanded, eerr := expandSubtreeProofPath(subLeaves, keyTail)
			if eerr != nil {
				t.Fatalf("expandSubtreeProofPath: %v", eerr)
			}
			t.Logf("expanded: %d nodes", len(expanded))
			for i, n := range expanded {
				hsum := sha3.NewLegacyKeccak256()
				hsum.Write(n)
				var hh [32]byte
				hsum.Sum(hh[:0])
				t.Logf("  expanded[%d]: len=%d keccak=%x", i, len(n), hh[:8])
				if hh == expectedHash {
					t.Logf("    ← MATCHES expected slot hash %x", expectedHash[:8])
				}
			}
		}
	}
}

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

