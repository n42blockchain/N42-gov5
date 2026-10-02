/*
   Tests for lib/common/address.go: construction, hex/checksum formatting,
   Format verbs, Marshal/Unmarshal, sql Scan/Value.
*/

package common

import (
	"fmt"
	"math/big"
	"testing"
)

func TestG35BytesToAddress(t *testing.T) {
	b := []byte{1, 2, 3}
	a := BytesToAddress(b)
	if a[19] != 3 || a[18] != 2 || a[17] != 1 {
		t.Fatalf("expected right-aligned bytes, got %x", a)
	}

	// longer than 20 bytes gets cropped from the left.
	long := make([]byte, 25)
	long[24] = 0xAB
	a2 := BytesToAddress(long)
	if a2[19] != 0xAB {
		t.Fatalf("expected last byte preserved, got %x", a2)
	}
}

func TestG35BigToAddress(t *testing.T) {
	a := BigToAddress(big.NewInt(255))
	if a[19] != 0xff {
		t.Fatalf("expected last byte 0xff, got %x", a)
	}
}

func TestG35HexToAddressAndIsHexAddress(t *testing.T) {
	a := HexToAddress("0x0000000000000000000000000000000000000001")
	if a[19] != 1 {
		t.Fatalf("expected address ending in 1, got %x", a)
	}

	if !IsHexAddress("0x0000000000000000000000000000000000000001") {
		t.Fatalf("expected valid hex address to be recognized")
	}
	if IsHexAddress("0x00") {
		t.Fatalf("expected short string to be rejected")
	}
	if IsHexAddress("0xzz00000000000000000000000000000000000001") {
		t.Fatalf("expected non-hex string to be rejected")
	}
	// without 0x prefix still works if length matches.
	if !IsHexAddress("0000000000000000000000000000000000000001") {
		t.Fatalf("expected unprefixed hex address to be recognized")
	}
}

func TestG35AddressBytesHashHex(t *testing.T) {
	a := HexToAddress("0x0102030000000000000000000000000000000a")
	if len(a.Bytes()) != 20 {
		t.Fatalf("expected 20-byte slice")
	}
	h := a.Hash()
	if h[31] != a[19] {
		t.Fatalf("expected address right-aligned in hash")
	}
	if a.Hex() == "" || a.String() != a.Hex() {
		t.Fatalf("expected String() == Hex()")
	}
}

func TestG35AddressFormat(t *testing.T) {
	a := HexToAddress("0x0000000000000000000000000000000000000001")
	if got := fmt.Sprintf("%v", a); got != a.Hex() {
		t.Fatalf("expected %%v to equal Hex(), got %s", got)
	}
	if got := fmt.Sprintf("%q", a); got[0] != '"' {
		t.Fatalf("expected %%q to be quoted, got %s", got)
	}
	if got := fmt.Sprintf("%x", a); len(got) != 40 {
		t.Fatalf("expected %%x to be 40 hex chars without 0x, got %q", got)
	}
	if got := fmt.Sprintf("%#x", a); len(got) != 42 {
		t.Fatalf("expected %%#x to include 0x prefix, got %q", got)
	}
	if got := fmt.Sprintf("%X", a); got == "" {
		t.Fatalf("expected %%X to produce output")
	}
	if got := fmt.Sprintf("%d", a); got == "" {
		t.Fatalf("expected %%d to produce output")
	}
	if got := fmt.Sprintf("%z", a); got == "" {
		t.Fatalf("expected default-case format to produce output")
	}
}

func TestG35AddressMarshalUnmarshal(t *testing.T) {
	a := HexToAddress("0x0000000000000000000000000000000000000042")
	text, err := a.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Address
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != a {
		t.Fatalf("expected round-trip equality, got %x != %x", back, a)
	}

	quoted := append(append([]byte{'"'}, text...), '"')
	var back2 Address
	if err := back2.UnmarshalJSON(quoted); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if back2 != a {
		t.Fatalf("expected JSON round-trip equality")
	}
}

func TestG35AddressScanValue(t *testing.T) {
	a := HexToAddress("0x0000000000000000000000000000000000000099")
	v, err := a.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	vb, ok := v.([]byte)
	if !ok || len(vb) != 20 {
		t.Fatalf("expected 20-byte driver.Value, got %v", v)
	}

	var back Address
	if err := back.Scan(vb); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if back != a {
		t.Fatalf("expected Scan round-trip equality")
	}

	if err := back.Scan("not-bytes"); err == nil {
		t.Fatalf("expected error scanning non-[]byte source")
	}
	if err := back.Scan([]byte{1, 2, 3}); err == nil {
		t.Fatalf("expected error scanning wrong-length bytes")
	}
}
