package mptproof

import (
	"bytes"
	"sort"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// TestDiagnose_V2_FailingSlots_HasExtension: for the 500-acct
// integration test's known-failing slots at branch [0], directly
// answer: "is this slot a single leaf, or a deeper subtree?"
//
// For each failing slot at prefix [0, X]:
//   1. Enumerate keccak matches under [0, X] from the same synthetic
//      input as the integration test.
//   2. Compute keccak(leafRLP(suffix, value)) for the FIRST match —
//      that is what V2 reconstructLeafHash returns for a leaf marker.
//   3. The integration test already shows V2's hash differs from V1's
//      stored hash for these slots; this test makes the *why* visible
//      by exposing the match count.
//
// Empirically (2026-05-21): every failing slot has 2+ matches, so
// its stored 33B is keccak(deeper-subtree-RLP), NOT a single leaf.
//
// The subtree could be (a) a branch with multiple direct children,
// (b) an extension wrapping a deeper branch, or (c) some combination.
// All three cases produce HasTree=0 in the parent slot's treeMask.
// The exact mechanism in gen_struct_step.go (Erigon port) involves
// two parent-tree-bit registration sites:
//
//   - line 287-288 (inside the h-callback block, guarded by
//     h != nil at line 278 AND maxLen != 0): SETS the bit
//     unconditionally when triggered.
//   - line 303-306 (inside the Branch-close block): SETS the
//     parent's hashMask bit always, but SETS the parent's treeMask
//     bit ONLY when hasTree[maxLen] != 0 (i.e. the current node has
//     a nested branch of its own).
//
// When a sub-branch's own children are all direct leaves
// (hasTree[maxLen] == 0), the line 303-306 route does not register
// a parent tree bit. The net effect: a slot can hold a 33B hash
// referencing a real branch while telling the parent "no deeper tree
// here," which V2's leaf-marker fast path then mistakes for a direct
// leaf reference.
//
// Path 2's OR propagation handles case (b) only partially. The fix
// for full V2 dispatch needs per-slot origin tracking that also
// flags "branchHash fired but my hasTree[maxLen] was 0" — not just
// "extensionHash fired."
func TestDiagnose_V2_FailingSlots_HasExtension(t *testing.T) {
	_, acctValues := makeAccountEntriesAndValues(500)

	type pair struct {
		keccak [32]byte
		value  []byte
	}
	var sorted []pair
	for addr, v := range acctValues {
		var k [32]byte
		h := sha3.NewLegacyKeccak256()
		h.Write(addr[:])
		copy(k[:], h.Sum(nil))
		sorted = append(sorted, pair{keccak: k, value: v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].keccak[:], sorted[j].keccak[:]) < 0
	})

	failingDigits := []byte{0, 2, 8, 9, 10, 11, 14, 15}
	for _, X := range failingDigits {
		prefix := []byte{0, X}
		var matches []pair
		for _, p := range sorted {
			if hasNibblePrefixBytes(p.keccak[:], prefix) {
				matches = append(matches, p)
			}
		}
		t.Logf("prefix [0, %d]: %d matches", X, len(matches))
		// The test's claim is "every listed failing slot has a multi-
		// leaf subtree behind it." If a future seed change collapses a
		// slot to a single leaf, the diagnose conclusion no longer
		// applies — fail loudly so the writeup gets re-validated.
		if len(matches) < 2 {
			t.Errorf("prefix [0, %d]: expected >=2 matches (multi-leaf subtree), got %d — re-validate failing-slot list and writeup",
				X, len(matches))
			continue
		}
		first := matches[0]
		suffix := make([]byte, 0, 65)
		for i, b := range first.keccak {
			hi, lo := b>>4, b&0xf
			if 2*i >= len(prefix) {
				suffix = append(suffix, hi)
			}
			if 2*i+1 >= len(prefix) {
				suffix = append(suffix, lo)
			}
		}
		suffix = append(suffix, 0x10)
		hash, _ := trie.LeafHashStandalone(suffix, rlphacks.RlpEncodedBytes(first.value))
		t.Logf("  first match keccak=%x value[:8]=%x reconstructed_hash=%x",
			first.keccak[:8], first.value[:8], hash[:8])
		for i, m := range matches {
			t.Logf("  match %d: keccak=%x", i, m.keccak[:16])
		}
	}
}
