package mptproof

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// TestVerify_Debug_TraceLeaf instruments the verify path for one
// synthetic case to find where computed/stored leaf hashes diverge.
func TestVerify_Debug_TraceLeaf(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 500)

	addr := addrs[0]
	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("addr      : %x", addr)
	t.Logf("hashedAddr: %x", proof.HashedAddr)
	t.Logf("leafValue : %x  (%d bytes)", proof.LeafValue, len(proof.LeafValue))
	t.Logf("expected leaf bytes: %x", values[addr])
	t.Logf("state root: %x", proof.StateRoot)
	t.Logf("walk hops : %d  outcome: %v  leafDepth: %d",
		len(proof.Walk.Hops), proof.Walk.Outcome, proof.Walk.LeafDepth)
	for i, hop := range proof.Walk.Hops {
		t.Logf("  hop %d depth=%d nibble=0x%x hasState=%016b hasTree=%016b hasHash=%016b hashes=%d hasRoot=%v",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, hop.Branch.HasTree, hop.Branch.HasHash,
			len(hop.Branch.Hashes), hop.Branch.HasRoot)
	}
	deepest := proof.Walk.Hops[len(proof.Walk.Hops)-1]
	storedHash, ok := deepest.Branch.ChildHash(deepest.TargetNibble)
	t.Logf("deepest hop child hash for nibble 0x%x: ok=%v hash=%x",
		deepest.TargetNibble, ok, storedHash)

	// Try several remainder lengths to find which matches.
	keyNibbles := nibblesOf(proof.HashedAddr[:])
	for d := proof.Walk.LeafDepth - 1; d <= proof.Walk.LeafDepth+1; d++ {
		if d < 0 || d > len(keyNibbles) {
			continue
		}
		remainder := keyNibbles[d:]
		h := computeLeafHash(remainder, proof.LeafValue)
		t.Logf("  trying leafDepth=%d: remainderLen=%d leafHash=%x match=%v",
			d, len(remainder), h, h == storedHash)
	}

	// Also try with different value encodings.
	t.Logf("trying value with extra RLP wrapping:")
	remainder := keyNibbles[proof.Walk.LeafDepth:]
	wrappedVal := encodeBytes(proof.LeafValue)
	h := computeLeafHash(remainder, wrappedVal)
	t.Logf("  encodeBytes-wrapped value: %x  leafHash=%x match=%v",
		wrappedVal, h, h == storedHash)
}

// TestVerify_SingleLeafIsLeafHash: when the trie has exactly one
// leaf, the stateRoot SHOULD equal that leaf's hash. Use this to
// directly compare HashBuilder's leaf-hash output vs our
// computeLeafHash without any branch encoding in the way.
func TestVerify_SingleLeafIsLeafHash(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 1)
	addr := addrs[0]
	value := values[addr]

	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := proof.StateRoot
	t.Logf("addr        : %x", addr)
	t.Logf("hashedAddr  : %x", proof.HashedAddr)
	t.Logf("value       : %x (%d bytes)", value, len(value))
	t.Logf("stateRoot   : %x", stateRoot)
	t.Logf("walk hops   : %d  leafDepth: %d  outcome: %v",
		len(proof.Walk.Hops), proof.Walk.LeafDepth, proof.Walk.Outcome)

	// For single-leaf trie the root IS the leaf hash (no branches).
	// Use full hashed key (depth 0) as remainder.
	keyNibbles := nibblesOf(proof.HashedAddr[:])
	myHash := computeLeafHash(keyNibbles, value)
	t.Logf("computeLeafHash(depth=0): %x", myHash)
	if myHash == stateRoot {
		t.Log("✓ MATCH at depth=0")
	}
}

// TestVerify_OracleCompare directly compares my computeLeafHash with
// lib/trie's exposed LeafHashStandalone for the same input.
func TestVerify_OracleCompare(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 200)
	addr := addrs[0]
	value := values[addr]

	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	// Build full keyHex (64 nibbles + 0x10 terminator).
	keyNibbles := nibblesOf(proof.HashedAddr[:])
	keyHex := append(append([]byte{}, keyNibbles...), 0x10)

	for ld := 0; ld <= 10; ld++ {
		if ld > len(keyHex) {
			break
		}
		subKey := keyHex[ld:]
		oracle, err := trie.LeafHashStandalone(subKey, rlphacks.RlpEncodedBytes(value))
		if err != nil {
			t.Logf("oracle err at depth=%d: %v", ld, err)
			continue
		}
		remainder := keyNibbles[ld:]
		mine := computeLeafHash(remainder, value)
		match := mine == oracle
		mark := "✗"
		if match {
			mark = "✓"
		}
		t.Logf("depth=%-2d  oracle=%x  mine=%x  %s",
			ld, oracle[:8], mine[:8], mark)
	}
}

func TestRLP_DebugPrint(t *testing.T) {
	// Print encoding of a 3-byte test value to compare with HashBuilder.
	v := []byte{0x00, 0x00, 0x55}
	t.Logf("encodeBytes(%x) = %s", v, hex.EncodeToString(encodeBytes(v)))
	t.Logf("expected per RlpEncodedBytes spec: 8300 0055 (4 bytes total)")
	fmt.Println("OK")
}
