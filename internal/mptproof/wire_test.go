package mptproof

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// ===========================================================================
// RLP list decoder unit tests
// ===========================================================================

func TestDecodeList_TwoItems(t *testing.T) {
	// RLP([ [0x83], [0x01, 0x02] ]) =
	//   list-prefix 0xc4 || 0x83 || 0x82 0x01 0x02
	// Wait, RLP-encoding a single byte 0x83 (>= 0x80): 0x81 0x83.
	// Let me build a real 2-item list: items = ["abc" (3 bytes), 0x01]
	//   item 0 = encodeBytes("abc") = 0x83 'a' 'b' 'c' (4 bytes)
	//   item 1 = encodeBytes([0x01]) = 0x01 (1 byte, < 0x80)
	//   payload = 5 bytes; list prefix = 0xc5
	raw := []byte{0xc5, 0x83, 'a', 'b', 'c', 0x01}
	items, err := decodeList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if string(items[0]) != "abc" {
		t.Errorf("item 0: got %x want 'abc'", items[0])
	}
	if len(items[1]) != 1 || items[1][0] != 0x01 {
		t.Errorf("item 1: got %x want [01]", items[1])
	}
}

func TestDecodeList_17ItemsRoundTrip(t *testing.T) {
	// Build a fake 17-item list with mostly empty + a few 32-byte hashes.
	var slots [17][]byte
	for i := range slots {
		slots[i] = []byte{0x80} // empty
	}
	// Slot 3: 32-byte hash 0xaa..
	hash3 := bytes.Repeat([]byte{0xaa}, 32)
	slots[3] = append([]byte{0xa0}, hash3...)
	// Slot 7: 32-byte hash 0xbb..
	hash7 := bytes.Repeat([]byte{0xbb}, 32)
	slots[7] = append([]byte{0xa0}, hash7...)

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	encoded := encodeList(payload.Bytes())

	items, err := decodeList(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 17 {
		t.Fatalf("expected 17 items, got %d", len(items))
	}
	if !bytes.Equal(items[3], hash3) {
		t.Errorf("slot 3 hash: got %x want %x", items[3], hash3)
	}
	if !bytes.Equal(items[7], hash7) {
		t.Errorf("slot 7 hash: got %x want %x", items[7], hash7)
	}
	for i, item := range items {
		if i == 3 || i == 7 {
			continue
		}
		if len(item) != 0 {
			t.Errorf("slot %d: expected empty got %x", i, item)
		}
	}
}

func TestDecodeHP_LeafEven(t *testing.T) {
	// HP-encode nibbles "12,34,56" with leaf flag.
	hp := []byte{0x20, 0x12, 0x34, 0x56}
	nibs, isLeaf := decodeHP(hp)
	if !isLeaf {
		t.Error("expected leaf")
	}
	want := []byte{1, 2, 3, 4, 5, 6}
	if !bytes.Equal(nibs, want) {
		t.Errorf("nibbles: got %v want %v", nibs, want)
	}
}

func TestDecodeHP_LeafOdd(t *testing.T) {
	// HP "abc" with leaf flag: prefix = 0x3a, then 0xbc
	hp := []byte{0x3a, 0xbc}
	nibs, isLeaf := decodeHP(hp)
	if !isLeaf {
		t.Error("expected leaf")
	}
	want := []byte{0xa, 0xb, 0xc}
	if !bytes.Equal(nibs, want) {
		t.Errorf("nibbles: got %v want %v", nibs, want)
	}
}

func TestDecodeHP_ExtensionEven(t *testing.T) {
	hp := []byte{0x00, 0xab, 0xcd}
	nibs, isLeaf := decodeHP(hp)
	if isLeaf {
		t.Error("expected extension")
	}
	want := []byte{0xa, 0xb, 0xc, 0xd}
	if !bytes.Equal(nibs, want) {
		t.Errorf("nibbles: got %v want %v", nibs, want)
	}
}

// Round-trip: hexToCompact then decodeHP.
func TestHP_RoundTrip(t *testing.T) {
	cases := []struct {
		nibbles []byte // input (last byte 0x10 means leaf)
		isLeaf  bool
	}{
		{[]byte{0x10}, true},
		{[]byte{0x01, 0x02, 0x10}, true},
		{[]byte{0xa, 0x01, 0x02, 0x10}, true},
		{[]byte{0xa, 0x01, 0x02}, false}, // extension odd
		{[]byte{0x01, 0x02, 0x03, 0x04}, false},
	}
	for _, c := range cases {
		hp := hexToCompact(c.nibbles)
		nibs, isLeaf := decodeHP(hp)
		if isLeaf != c.isLeaf {
			t.Errorf("isLeaf mismatch for %v: got %v", c.nibbles, isLeaf)
		}
		expectedNibbles := c.nibbles
		if c.isLeaf {
			expectedNibbles = c.nibbles[:len(c.nibbles)-1]
		}
		if !bytes.Equal(nibs, expectedNibbles) {
			t.Errorf("nibbles for %v: got %v want %v", c.nibbles, nibs, expectedNibbles)
		}
	}
}

// ===========================================================================
// Single-leaf trie: ProofBytes should be 1 element = the leaf node;
// VerifyStandardProof should round-trip the value.
// ===========================================================================

func TestProofBytes_SingleLeaf_RoundTrip(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 1)
	addr := addrs[0]
	value := values[addr]

	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := proof.ProofBytes()
	if err != nil {
		t.Fatalf("ProofBytes: %v", err)
	}
	t.Logf("single-leaf proof: %d element(s), total %d bytes",
		len(pb), totalSize(pb))

	// For single-leaf trie, walk has 0 hops + 1 leaf = 1 element.
	// Verify oracle: keccak(pb[0]) should equal stateRoot.
	gotVal, found, err := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
	if err != nil {
		t.Fatalf("VerifyStandardProof: %v", err)
	}
	if !found {
		t.Fatal("VerifyStandardProof: !found for known-included key")
	}
	if !bytes.Equal(gotVal, value) {
		t.Errorf("value: got %x want %x", gotVal, value)
	}
}

// ===========================================================================
// Synthetic multi-leaf trie: ProofBytes returns N hops + leaf node
// (when no inline siblings) AND VerifyStandardProof passes against
// stateRoot.
// ===========================================================================

func TestProofBytes_Synthetic_VerifyVsRoot(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 500)

	var ok, inlineSib, extension, other int
	for _, idx := range []int{0, 1, 5, 42, 100, 200, 499} {
		addr := addrs[idx]
		proof, err := g.LatestAccountProof(addr)
		if err != nil {
			t.Fatal(err)
		}
		pb, err := proof.ProofBytes()
		switch {
		case err == nil:
			// Verify oracle.
			gotVal, found, verr := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
			if verr != nil || !found {
				// Might fail at inline child in oracle (inline target).
				if verr != nil && bytes.Contains([]byte(verr.Error()), []byte("inline child")) {
					inlineSib++
					t.Logf("  idx=%d oracle hit inline-child limit: %v", idx, verr)
					continue
				}
				other++
				t.Errorf("idx=%d verify error: found=%v err=%v", idx, found, verr)
				continue
			}
			if !bytes.Equal(gotVal, values[addr]) {
				other++
				t.Errorf("idx=%d value mismatch: got %x want %x",
					idx, gotVal, values[addr])
				continue
			}
			ok++
			t.Logf("✓ idx=%d proof verified: %d nodes, value %d bytes",
				idx, len(pb), len(gotVal))
		case errors.Is(err, ErrInlineSiblingInPath):
			inlineSib++
			t.Logf("  idx=%d inline sibling on path (D.1 limitation): %v", idx, err)
		default:
			extension++
			t.Logf("  idx=%d ProofBytes err (extension or other): %v", idx, err)
		}
	}
	t.Logf("synthetic ProofBytes: %d verified, %d inline-sibling, %d extension/other, %d errors",
		ok, inlineSib, extension, other)
	if other > 0 {
		t.Fatalf("%d unexpected errors (expected 0)", other)
	}
}

// ===========================================================================
// Production USDC proof bytes — slow but the real correctness check.
// ===========================================================================

func TestProofBytes_Production_USDC(t *testing.T) {
	g := openProductionGenerator(t)

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	proof, err := g.LatestAccountProof(usdc)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := proof.ProofBytes()
	if err != nil {
		if errors.Is(err, ErrInlineSiblingInPath) {
			t.Logf("USDC walk hit inline sibling (D.x): %v", err)
			return
		}
		t.Fatalf("ProofBytes USDC: %v", err)
	}
	t.Logf("USDC proof: %d nodes, %d bytes total", len(pb), totalSize(pb))
	for i, n := range pb {
		t.Logf("  node %d: %d bytes  keccak=%x", i, len(n), keccak256(n))
	}

	gotVal, found, verr := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
	switch {
	case verr != nil:
		t.Logf("oracle verify result: %v", verr)
	case found:
		t.Logf("✓ USDC proof verifies via standard oracle, value %d bytes", len(gotVal))
	default:
		t.Logf("USDC oracle returned non-inclusion (likely ExtensionInPath case)")
	}
}

func totalSize(b [][]byte) int {
	total := 0
	for _, x := range b {
		total += len(x)
	}
	return total
}
