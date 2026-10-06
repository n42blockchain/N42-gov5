package mptproof

import (
	"bytes"
	"testing"
)

// TestDecodeRethAccount covers the bitflag-packed account codec with
// nonce/balance of varying lengths and with/without a bytecode hash.
func TestDecodeRethAccount(t *testing.T) {
	// nonceLen=0, balanceLen=0, no bytecode: buf = [0x00, 0x00]
	acc, err := DecodeRethAccount([]byte{0x00, 0x00})
	if err != nil {
		t.Fatalf("empty account decode: %v", err)
	}
	if acc.Nonce != 0 || !acc.Balance.IsZero() || acc.HasBytecode {
		t.Fatalf("expected zero account, got %+v", acc)
	}

	// nonceLen=1 (bits0-3=1), balanceLen=1 (bits4-9=1 -> f0 bit4=1, f1 bits0-1=0),
	// hasBytecode bit (bit2 of byte1) = 1.
	f0 := byte(1) | (1 << 4) // nonceLen=1, low 2 bits of balanceLen=01
	f1 := byte(0x04)         // hasBytecode bit set, remaining balanceLen bits = 0
	buf := []byte{f0, f1, 0x07, 0x09}
	var bc [32]byte
	for i := range bc {
		bc[i] = byte(i + 1)
	}
	buf = append(buf, bc[:]...)
	acc2, err := DecodeRethAccount(buf)
	if err != nil {
		t.Fatalf("decode with bytecode: %v", err)
	}
	if acc2.Nonce != 0x07 {
		t.Fatalf("nonce = %d, want 7", acc2.Nonce)
	}
	if acc2.Balance.Uint64() != 0x09 {
		t.Fatalf("balance = %v, want 9", acc2.Balance.Uint64())
	}
	if !acc2.HasBytecode || !bytes.Equal(acc2.BytecodeHash[:], bc[:]) {
		t.Fatalf("bytecode hash mismatch: %+v", acc2)
	}

	// Too short for bitflags.
	if _, err := DecodeRethAccount([]byte{0x00}); err == nil {
		t.Fatalf("expected error for 1-byte buffer")
	}

	// Out-of-range nonce length (> 8).
	badF0 := byte(0x0f) // nonceLen = 15
	if _, err := DecodeRethAccount([]byte{badF0, 0x00}); err == nil {
		t.Fatalf("expected error for out-of-range nonce length")
	}

	// Declared length longer than buffer.
	if _, err := DecodeRethAccount([]byte{0x01, 0x00}); err == nil {
		t.Fatalf("expected error for truncated buffer")
	}
}

// TestStoredNibblesRoundTrip covers EncodeStoredNibbles/DecodeStoredNibbles
// and their rejection paths.
func TestStoredNibblesRoundTrip(t *testing.T) {
	nib := []byte{1, 2, 3, 4, 5}
	enc := EncodeStoredNibbles(nib)
	if len(enc) != 65 {
		t.Fatalf("encoded length = %d, want 65", len(enc))
	}
	dec := DecodeStoredNibbles(enc)
	if !bytes.Equal(dec, nib) {
		t.Fatalf("round trip mismatch: got %v want %v", dec, nib)
	}

	// Empty nibble path round trips to an empty (non-nil-length) slice.
	empty := EncodeStoredNibbles(nil)
	if len(empty) != 65 || empty[64] != 0 {
		t.Fatalf("empty encode malformed: %v", empty)
	}
	if got := DecodeStoredNibbles(empty); len(got) != 0 {
		t.Fatalf("empty decode = %v, want empty", got)
	}

	// Over-length nibbles rejected.
	if EncodeStoredNibbles(make([]byte, 65)) != nil {
		t.Fatalf("expected nil for >64 nibbles")
	}

	// Buffer too short to decode.
	if DecodeStoredNibbles(make([]byte, 10)) != nil {
		t.Fatalf("expected nil for short buffer")
	}

	// Length byte clamps at 64 even if larger is stored.
	raw := make([]byte, 65)
	raw[64] = 200
	if got := DecodeStoredNibbles(raw); len(got) != 64 {
		t.Fatalf("expected clamp to 64, got %d", len(got))
	}
}

// TestDecodeRethBranchNodeCompact covers the mask+hash wire format,
// both with and without an embedded root hash, and the error paths.
func TestDecodeRethBranchNodeCompact(t *testing.T) {
	// hash_mask selects bits 0 and 2 -> 2 hashes, no root hash.
	hashMask := uint16(0b0101)
	raw := make([]byte, 6)
	raw[0], raw[1] = 0x00, 0x00 // state mask
	raw[2], raw[3] = 0x00, 0x00 // tree mask
	raw[4], raw[5] = byte(hashMask), byte(hashMask>>8)
	var h0, h1 [32]byte
	h0[0] = 0xAA
	h1[0] = 0xBB
	raw = append(raw, h0[:]...)
	raw = append(raw, h1[:]...)

	bn, err := DecodeRethBranchNodeCompact(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bn.HasRoot {
		t.Fatalf("expected no root hash")
	}
	if len(bn.Hashes) != 2 || bn.Hashes[0] != h0 || bn.Hashes[1] != h1 {
		t.Fatalf("hashes mismatch: %+v", bn.Hashes)
	}

	// With root hash: hashCount == expected+1.
	var root [32]byte
	root[0] = 0xCC
	rawRoot := append(append([]byte{}, raw[:6]...), root[:]...)
	rawRoot = append(rawRoot, h0[:]...)
	rawRoot = append(rawRoot, h1[:]...)
	bn2, err := DecodeRethBranchNodeCompact(rawRoot)
	if err != nil {
		t.Fatalf("decode with root: %v", err)
	}
	if !bn2.HasRoot || bn2.RootHash != root {
		t.Fatalf("expected root hash %x, got %+v", root, bn2)
	}

	// Too short.
	if _, err := DecodeRethBranchNodeCompact([]byte{1, 2, 3}); err == nil {
		t.Fatalf("expected error for short buffer")
	}

	// Payload not a multiple of 32.
	bad := append(append([]byte{}, raw[:6]...), make([]byte, 10)...)
	if _, err := DecodeRethBranchNodeCompact(bad); err == nil {
		t.Fatalf("expected error for misaligned payload")
	}

	// Hash count mismatch (neither expected nor expected+1).
	badCount := append(append([]byte{}, raw[:6]...), h0[:]...)
	if _, err := DecodeRethBranchNodeCompact(badCount); err == nil {
		t.Fatalf("expected error for hash count mismatch")
	}
}

func TestPackedAndVariableSize(t *testing.T) {
	if PackedSize(0) != 33 || PackedSize(64) != 33 {
		t.Fatalf("PackedSize should always be 33")
	}
	cases := map[int]int{0: 1, 1: 2, 2: 2, 62: 32, 63: 33, 64: 33}
	for n, want := range cases {
		if got := VariableSize(n); got != want {
			t.Fatalf("VariableSize(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestStripLeadingZeros(t *testing.T) {
	cases := []struct {
		in, want []byte
	}{
		{[]byte{0, 0, 1, 2}, []byte{1, 2}},
		{[]byte{0, 0, 0}, []byte{}},
		{[]byte{5, 0, 6}, []byte{5, 0, 6}},
		{[]byte{}, []byte{}},
	}
	for _, c := range cases {
		got := stripLeadingZeros(c.in)
		if !bytes.Equal(got, c.want) {
			t.Fatalf("stripLeadingZeros(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHasNibblePrefixAndCompare(t *testing.T) {
	nib := []byte{1, 2, 3, 4}
	if !hasNibblePrefix(nib, []byte{1, 2}) {
		t.Fatalf("expected prefix match")
	}
	if hasNibblePrefix(nib, []byte{1, 3}) {
		t.Fatalf("expected prefix mismatch")
	}
	if hasNibblePrefix([]byte{1}, []byte{1, 2}) {
		t.Fatalf("prefix longer than nib must not match")
	}
	if hasNibblePrefix(nib, nil) != true {
		t.Fatalf("empty prefix always matches")
	}

	if compareNibbles([]byte{1, 2}, []byte{1, 3}) >= 0 {
		t.Fatalf("expected a < b")
	}
	if compareNibbles([]byte{1, 3}, []byte{1, 2}) <= 0 {
		t.Fatalf("expected a > b")
	}
	if compareNibbles([]byte{1, 2}, []byte{1, 2}) != 0 {
		t.Fatalf("expected equal")
	}
	if compareNibbles([]byte{1, 2}, []byte{1, 2, 3}) >= 0 {
		t.Fatalf("shorter prefix should sort before longer")
	}
	if compareNibbles([]byte{1, 2, 3}, []byte{1, 2}) <= 0 {
		t.Fatalf("longer should sort after shorter prefix")
	}
}

// TestVerifyProofChain exercises the internal structural proof-chain
// sanity check: a well-formed hash-linked chain passes, a broken
// link is reported at the correct index.
func TestVerifyProofChain(t *testing.T) {
	leaf := []byte("leaf-rlp-payload")
	leafHash := keccak256(leaf)
	var marker [33]byte
	marker[0] = 0xa0
	copy(marker[1:], leafHash[:])
	parent := append([]byte{0xc0}, marker[:]...)

	if idx := VerifyProofChain([][]byte{parent, leaf}); idx != -1 {
		t.Fatalf("expected valid chain, got break at %d", idx)
	}

	// Single-element chain is trivially valid.
	if idx := VerifyProofChain([][]byte{leaf}); idx != -1 {
		t.Fatalf("expected valid single-element chain, got %d", idx)
	}

	// Tamper with the parent's embedded hash -> break detected at index 0.
	tamperedParent := append([]byte{}, parent...)
	tamperedParent[2] ^= 0xFF
	if idx := VerifyProofChain([][]byte{tamperedParent, leaf}); idx != 0 {
		t.Fatalf("expected break at index 0, got %d", idx)
	}
}
