/*
   Tests for lib/common/hash.go: construction, hex/formatting, Marshal/Unmarshal,
   sql Scan/Value, Generate, and FromHex.
*/

package common

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"
)

func TestG35BytesToHashAndCastToHash(t *testing.T) {
	b := []byte{1, 2, 3}
	h := BytesToHash(b)
	if h[31] != 3 {
		t.Fatalf("expected right-aligned bytes, got %x", h)
	}

	full := make([]byte, 32)
	full[0] = 0xAB
	h2 := CastToHash(full)
	if h2[0] != 0xAB {
		t.Fatalf("expected CastToHash to reinterpret bytes directly")
	}
}

func TestG35BigToHashHexToHash(t *testing.T) {
	h := BigToHash(big.NewInt(255))
	if h[31] != 0xff {
		t.Fatalf("expected last byte 0xff, got %x", h)
	}
	h2 := HexToHash("0x01")
	if h2[31] != 1 {
		t.Fatalf("expected last byte 1, got %x", h2)
	}
}

func TestG35HashBytesBigHex(t *testing.T) {
	h := HexToHash("0xff")
	if len(h.Bytes()) != 32 {
		t.Fatalf("expected 32-byte slice")
	}
	if h.Big().Int64() != 255 {
		t.Fatalf("expected big value 255, got %v", h.Big())
	}
	if h.Hex() == "" {
		t.Fatalf("expected non-empty Hex()")
	}
	if h.String() != h.Hex() {
		t.Fatalf("expected String() == Hex()")
	}
}

func TestG35HashTerminalString(t *testing.T) {
	h := HexToHash("0x0102030000000000000000000000000000000000000000000000000000000a0b0c")
	ts := h.TerminalString()
	if ts == "" {
		t.Fatalf("expected non-empty terminal string")
	}
}

func TestG35HashFormat(t *testing.T) {
	h := HexToHash("0x01")
	if got := fmt.Sprintf("%v", h); got == "" {
		t.Fatalf("expected %%v output")
	}
	if got := fmt.Sprintf("%s", h); got == "" {
		t.Fatalf("expected %%s output")
	}
	if got := fmt.Sprintf("%q", h); got[0] != '"' {
		t.Fatalf("expected %%q to be quoted")
	}
	if got := fmt.Sprintf("%x", h); len(got) != 64 {
		t.Fatalf("expected 64 hex chars for %%x, got %d", len(got))
	}
	if got := fmt.Sprintf("%#x", h); len(got) != 66 {
		t.Fatalf("expected 0x-prefixed %%#x, got %q", got)
	}
	if got := fmt.Sprintf("%X", h); got == "" {
		t.Fatalf("expected %%X output")
	}
	if got := fmt.Sprintf("%d", h); got == "" {
		t.Fatalf("expected %%d output")
	}
	if got := fmt.Sprintf("%z", h); got == "" {
		t.Fatalf("expected default-case output")
	}
}

func TestG35HashMarshalUnmarshal(t *testing.T) {
	h := HexToHash("0x42")
	text, err := h.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Hash
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != h {
		t.Fatalf("expected round-trip equality")
	}

	quoted := append(append([]byte{'"'}, text...), '"')
	var back2 Hash
	if err := back2.UnmarshalJSON(quoted); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if back2 != h {
		t.Fatalf("expected JSON round-trip equality")
	}
}

func TestG35HashScanValue(t *testing.T) {
	h := HexToHash("0x99")
	v, err := h.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	vb := v.([]byte)
	var back Hash
	if err := back.Scan(vb); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if back != h {
		t.Fatalf("expected Scan round-trip equality")
	}
	if err := back.Scan("nope"); err == nil {
		t.Fatalf("expected error scanning non-[]byte")
	}
	if err := back.Scan([]byte{1}); err == nil {
		t.Fatalf("expected error scanning wrong length")
	}
}

func TestG35HashGenerate(t *testing.T) {
	var h Hash
	r := rand.New(rand.NewSource(1))
	v := h.Generate(r, 1)
	if _, ok := v.Interface().(Hash); !ok {
		t.Fatalf("expected Generate to return a Hash value")
	}
}

func TestG35FromHex(t *testing.T) {
	b := FromHex("0x0102")
	if len(b) != 2 || b[0] != 1 || b[1] != 2 {
		t.Fatalf("expected [1,2], got %v", b)
	}
}
