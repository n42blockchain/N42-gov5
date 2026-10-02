package trie

import (
	"bytes"
	"testing"

	rlp2 "github.com/n42blockchain/N42/lib/rlp"
)

func TestHexCompactRoundTrip(t *testing.T) {
	tests := []struct{ hex, compact []byte }{
		// odd length, no terminator
		{[]byte{1, 2, 3, 4, 5}, []byte{0x11, 0x23, 0x45}},
		// even length, no terminator
		{[]byte{0, 1, 2, 3, 4, 5}, []byte{0x00, 0x01, 0x23, 0x45}},
		// odd length, terminator
		{[]byte{15, 1, 12, 11, 8, 16}, []byte{0x3f, 0x1c, 0xb8}},
		// even length, terminator
		{[]byte{0, 15, 1, 12, 11, 8, 16}, []byte{0x20, 0x0f, 0x1c, 0xb8}},
	}
	for i, tc := range tests {
		got := hexToCompact(tc.hex)
		if !bytes.Equal(got, tc.compact) {
			t.Errorf("case %d: hexToCompact = %x, want %x", i, got, tc.compact)
		}
		back := compactToHex(tc.compact)
		if !bytes.Equal(back, tc.hex) {
			t.Errorf("case %d: compactToHex = %x, want %x", i, back, tc.hex)
		}
	}
}

func TestCompactToHexEmpty(t *testing.T) {
	if got := compactToHex(nil); len(got) != 0 {
		t.Errorf("compactToHex(nil) = %x, want empty", got)
	}
}

func TestHexKeybytesRoundTrip(t *testing.T) {
	keys := [][]byte{
		{},
		{0x12, 0x34, 0x56},
		{0x00},
		{0xff, 0xff, 0xff, 0xff},
	}
	for _, key := range keys {
		hex := keybytesToHex(key)
		if !hasTerm(hex) {
			t.Errorf("keybytesToHex(%x) missing terminator", key)
		}
		back := hexToKeybytes(hex)
		if !bytes.Equal(back, key) {
			t.Errorf("hexToKeybytes(keybytesToHex(%x)) = %x, want %x", key, back, key)
		}
	}
}

func TestHexToKeybytesOddPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for odd-length hex key")
		}
	}()
	hexToKeybytes([]byte{1, 2, 3})
}

func TestPrefixLen(t *testing.T) {
	cases := []struct {
		a, b []byte
		want int
	}{
		{[]byte{1, 2, 3}, []byte{1, 2, 4}, 2},
		{[]byte{1, 2, 3}, []byte{1, 2, 3}, 3},
		{[]byte{}, []byte{1}, 0},
		{[]byte{1, 2, 3}, []byte{1, 2}, 2},
	}
	for _, c := range cases {
		got := prefixLen(c.a, c.b)
		if got != c.want {
			t.Errorf("prefixLen(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestHasTerm(t *testing.T) {
	if hasTerm(nil) {
		t.Error("hasTerm(nil) should be false")
	}
	if hasTerm([]byte{1, 2, 3}) {
		t.Error("hasTerm without 16 at end should be false")
	}
	if !hasTerm([]byte{1, 2, 16}) {
		t.Error("hasTerm with trailing 16 should be true")
	}
}

func TestKeybytesStruct(t *testing.T) {
	cases := []struct {
		data        []byte
		odd         bool
		terminating bool
	}{
		{[]byte{0x12, 0x34}, false, false},
		{[]byte{0x12, 0x34}, false, true},
		{[]byte{0x1, 0x23}, true, false},
		{[]byte{0x1, 0x23}, true, true},
		{[]byte{}, false, true},
	}
	for i, c := range cases {
		k := Keybytes{Data: c.data, Odd: c.odd, Terminating: c.terminating}
		wantNibbles := len(c.data) * 2
		if c.odd {
			wantNibbles--
		}
		if n := k.Nibbles(); n != wantNibbles {
			t.Errorf("case %d: Nibbles() = %d, want %d", i, n, wantNibbles)
		}
		compact := k.ToCompact()
		back := CompactToKeybytes(compact)
		if back.Odd != k.Odd {
			t.Errorf("case %d: Odd mismatch: got %v want %v", i, back.Odd, k.Odd)
		}
		if back.Terminating != k.Terminating {
			t.Errorf("case %d: Terminating mismatch: got %v want %v", i, back.Terminating, k.Terminating)
		}
		hex := k.ToHex()
		if c.terminating != hasTerm(hex) {
			t.Errorf("case %d: ToHex() terminator mismatch", i)
		}
	}
}

func TestKeybytesEncodeDecodeRLP(t *testing.T) {
	k := Keybytes{Data: []byte{0xab, 0xcd}, Odd: false, Terminating: true}
	var buf bytes.Buffer
	if err := k.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP error: %v", err)
	}

	var k2 Keybytes
	stream := rlp2.NewStream(&buf, 0)
	if err := k2.DecodeRLP(stream); err != nil {
		t.Fatalf("DecodeRLP error: %v", err)
	}
	if !bytes.Equal(k2.ToCompact(), k.ToCompact()) {
		t.Errorf("round-trip mismatch: got %x want %x", k2.ToCompact(), k.ToCompact())
	}
}
