package mptproof

import (
	"bytes"
	"testing"
)

// TestPackNibblesV2_RoundTrip: round-trip nibble paths of all
// length-parity cases.
func TestPackNibblesV2_RoundTrip(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0},
		{0, 1},
		{0, 1, 2},
		{0xa, 0xb, 0xc, 0xd},
		{0xf, 0xe, 0xd, 0xc, 0xb, 0xa, 0x9, 0x8, 0x7, 0x6, 0x5, 0x4, 0x3, 0x2, 0x1, 0x0},
		make([]byte, 64),
	}
	for i, c := range cases {
		// Make sure cases[7] = make([]byte, 64) has values in [0,15].
		for j := range c {
			if c[j] > 15 {
				c[j] = c[j] & 0x0f
			}
		}
		packed, err := PackNibblesV2(c)
		if err != nil {
			t.Fatalf("case %d: pack: %v", i, err)
		}
		if len(packed) != 33 {
			t.Fatalf("case %d: packed len %d != 33", i, len(packed))
		}
		back, err := UnpackNibblesV2(packed)
		if err != nil {
			t.Fatalf("case %d: unpack: %v", i, err)
		}
		want := c
		if c == nil {
			want = []byte{}
		}
		if !bytes.Equal(back, want) {
			t.Errorf("case %d: round-trip mismatch\n  in:  %x\n  out: %x", i, want, back)
		}
	}
}

// TestPackNibblesV2_OrderingPreserved: memcmp of packed forms must
// match lexicographic ordering of original nibble sequences (so
// MDBX DupSort can use the packed key as subkey directly).
func TestPackNibblesV2_OrderingPreserved(t *testing.T) {
	// Pairs (a, b) where a should sort before b under nibble-lex.
	pairs := [][2][]byte{
		{{}, {0}},
		{{0}, {1}},
		{{1, 0}, {1, 1}},
		{{1, 2}, {1, 2, 0}},     // shorter < longer when prefix matches
		{{1, 2, 0}, {1, 2, 1}}, // both 3-nib, differ at last
		{{0xa, 0xb}, {0xa, 0xc}},
	}
	for i, p := range pairs {
		pa, err1 := PackNibblesV2(p[0])
		pb, err2 := PackNibblesV2(p[1])
		if err1 != nil || err2 != nil {
			t.Fatalf("pair %d: pack err: %v / %v", i, err1, err2)
		}
		if bytes.Compare(pa, pb) >= 0 {
			t.Errorf("pair %d: %x !< %x (packed cmp = %d)",
				i, p[0], p[1], bytes.Compare(pa, pb))
		}
	}
}

// TestPackNibblesV2_RejectsBadInput: malformed inputs.
func TestPackNibblesV2_RejectsBadInput(t *testing.T) {
	if _, err := PackNibblesV2(make([]byte, 65)); err == nil {
		t.Errorf("expected error on 65-nibble input")
	}
	if _, err := PackNibblesV2([]byte{0x10}); err == nil {
		t.Errorf("expected error on nibble > 15")
	}
	if _, err := UnpackNibblesV2(make([]byte, 32)); err == nil {
		t.Errorf("expected error on 32-byte input (need 33)")
	}
}
