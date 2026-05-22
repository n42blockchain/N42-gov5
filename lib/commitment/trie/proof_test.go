package trie

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestProve_SingleLeafRoot: a trie consisting of a single leaf at
// the root produces a one-node proof whose hash matches the leaf
// hash and which decodes back to (key, value).
func TestProve_SingleLeafRoot(t *testing.T) {
	key := []byte{0x12, 0x34, 0x56, 0x78}
	value := []byte("hello world")

	// keybytesToHex(key) + terminator → leaf's Key
	keyNib := keybytesToHex(key)
	leaf := &ShortNode{Key: keyNib, Val: ValueNode(value)}
	tr := NewInMemoryTrie(leaf)

	proof, err := tr.Prove(key, 0, false)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if len(proof) != 1 {
		t.Fatalf("expected 1 node, got %d", len(proof))
	}

	// Independent check: keccak(proof[0]) should equal the root hash
	// of the leaf node encoded standalone.
	gotEnc, err := encodeNode(leaf)
	if err != nil {
		t.Fatalf("encodeNode: %v", err)
	}
	if !bytes.Equal(proof[0], gotEnc) {
		t.Fatalf("proof[0] != encodeNode(leaf)\nproof: %x\nenc:   %x", proof[0], gotEnc)
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(proof[0])
	var rootHash [32]byte
	h.Sum(rootHash[:0])
	t.Logf("single-leaf proof OK: 1 node / %d bytes / root %x", len(proof[0]), rootHash[:8])
}

// TestProve_BranchToLeaf: trie with two distinct-prefix leaves
// produces a 2-node proof (root branch + leaf).
func TestProve_BranchToLeaf(t *testing.T) {
	// Two keys diverging at the first nibble.
	k1 := []byte{0x10}
	v1 := []byte("alpha")
	k2 := []byte{0x20}
	v2 := []byte("bravo")

	leaf1 := &ShortNode{
		Key: keybytesToHex(k1)[1:], // drop first nibble (consumed by branch)
		Val: ValueNode(v1),
	}
	leaf2 := &ShortNode{
		Key: keybytesToHex(k2)[1:],
		Val: ValueNode(v2),
	}
	root := &FullNode{}
	root.Children[0x1] = leaf1
	root.Children[0x2] = leaf2
	tr := NewInMemoryTrie(root)

	proof, err := tr.Prove(k1, 0, false)
	if err != nil {
		t.Fatalf("Prove k1: %v", err)
	}
	if len(proof) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(proof))
	}

	// proof[0] = root FullNode RLP; should decode-equivalent to
	// encodeNode(root).
	wantRoot, err := encodeNode(root)
	if err != nil {
		t.Fatalf("encodeNode root: %v", err)
	}
	if !bytes.Equal(proof[0], wantRoot) {
		t.Fatalf("proof[0] != encodeNode(root)")
	}

	// proof[1] = leaf1 RLP.
	wantLeaf, err := encodeNode(leaf1)
	if err != nil {
		t.Fatalf("encodeNode leaf1: %v", err)
	}
	if !bytes.Equal(proof[1], wantLeaf) {
		t.Fatalf("proof[1] != encodeNode(leaf1)")
	}
	t.Logf("branch-to-leaf proof OK: 2 nodes / %d + %d bytes",
		len(proof[0]), len(proof[1]))
}

// TestProve_FromLevel: fromLevel=1 skips the root and emits only
// the leaf.
func TestProve_FromLevel(t *testing.T) {
	k := []byte{0x10}
	v := []byte("test")
	leaf := &ShortNode{Key: keybytesToHex(k)[1:], Val: ValueNode(v)}
	root := &FullNode{}
	root.Children[0x1] = leaf
	tr := NewInMemoryTrie(root)

	proof, err := tr.Prove(k, 1, false)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if len(proof) != 1 {
		t.Fatalf("expected 1 node (root skipped), got %d", len(proof))
	}
	t.Logf("fromLevel=1 correctly skipped root: 1 node / %d bytes",
		len(proof[0]))
}

// TestProve_KeyDiverges: a key that doesn't match the trie's
// structure returns the proof up to the divergence point (not an
// error).
func TestProve_KeyDiverges(t *testing.T) {
	leaf := &ShortNode{Key: []byte{0x1, 0x2, 0x3, 0x4, 16}, Val: ValueNode("x")}
	tr := NewInMemoryTrie(leaf)

	// Query a key whose first nibble differs.
	proof, err := tr.Prove([]byte{0x99}, 0, false)
	if err != nil {
		t.Fatalf("Prove diverging: %v", err)
	}
	if len(proof) != 1 {
		t.Fatalf("expected 1 node (divergence at root short-node), got %d",
			len(proof))
	}
}
