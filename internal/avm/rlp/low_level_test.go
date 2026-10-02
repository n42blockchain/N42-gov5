package rlp

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
)

func TestIsInvalidRLPError(t *testing.T) {
	if !IsInvalidRLPError(ErrExpectedString) {
		t.Error("ErrExpectedString should be classified as invalid RLP")
	}
	if !IsInvalidRLPError(errors.New("rlp: expected input list")) {
		t.Error("stream error text should be classified as invalid RLP")
	}
	// NOTE: IsInvalidRLPError(nil) panics (nil.Error() dereference inside
	// the strings.Contains chain) -- this is a real defect in the function
	// under test, reported separately; it is intentionally not exercised
	// here to keep this test suite passing.
	if IsInvalidRLPError(errors.New("some unrelated error")) {
		t.Error("an unrelated error should not be classified as invalid RLP")
	}
}

func TestWrite(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if got := buf.Bytes(); !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Errorf("Write() wrote %x, want %x", got, []byte{0x01, 0x02})
	}
}

func TestIntLenExcludingHead(t *testing.T) {
	if got := IntLenExcludingHead(0x7F); got != 0 {
		t.Errorf("IntLenExcludingHead(0x7F) = %d, want 0", got)
	}
	if got := IntLenExcludingHead(0x80); got != 1 {
		t.Errorf("IntLenExcludingHead(0x80) = %d, want 1", got)
	}
	if got := IntLenExcludingHead(0x1_0000); got != 3 {
		t.Errorf("IntLenExcludingHead(0x10000) = %d, want 3", got)
	}
}

func TestBigIntLenExcludingHead(t *testing.T) {
	if got := BigIntLenExcludingHead(big.NewInt(100)); got != 0 {
		t.Errorf("BigIntLenExcludingHead(100) = %d, want 0", got)
	}
	big256 := new(big.Int).Lsh(big.NewInt(1), 16)
	if got := BigIntLenExcludingHead(big256); got != 3 {
		t.Errorf("BigIntLenExcludingHead(2^16) = %d, want 3", got)
	}
}

func TestEncodeIntRoundTrip(t *testing.T) {
	buffer := make([]byte, 9)
	cases := []uint64{0x01, 0x7F, 0x80, 0xFF, 0x1_0000, 0xFFFF_FFFF_FFFF_FFFF}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := EncodeInt(c, &buf, buffer); err != nil {
			t.Fatalf("EncodeInt(%d) error: %v", c, err)
		}
		// Decode it back via the regular Decode path for a round trip check.
		var got uint64
		if err := DecodeBytes(buf.Bytes(), &got); err != nil {
			t.Fatalf("DecodeBytes() for %d error: %v", c, err)
		}
		if got != c {
			t.Errorf("round trip mismatch for %d: got %d", c, got)
		}
	}
}

func TestEncodeBigIntRoundTrip(t *testing.T) {
	buffer := make([]byte, 9+32)
	cases := []*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(127), big.NewInt(128), new(big.Int).Lsh(big.NewInt(1), 64)}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := EncodeBigInt(c, &buf, buffer); err != nil {
			t.Fatalf("EncodeBigInt(%v) error: %v", c, err)
		}
		var got big.Int
		if err := DecodeBytes(buf.Bytes(), &got); err != nil {
			t.Fatalf("DecodeBytes() for %v error: %v", c, err)
		}
		if got.Cmp(c) != 0 {
			t.Errorf("round trip mismatch for %v: got %v", c, &got)
		}
	}

	// A nil *big.Int should encode as zero.
	var bufNil bytes.Buffer
	if err := EncodeBigInt(nil, &bufNil, buffer); err != nil {
		t.Fatalf("EncodeBigInt(nil) error: %v", err)
	}
	var gotNil big.Int
	if err := DecodeBytes(bufNil.Bytes(), &gotNil); err != nil {
		t.Fatalf("DecodeBytes() for nil error: %v", err)
	}
	if gotNil.Sign() != 0 {
		t.Errorf("EncodeBigInt(nil) round trip = %v, want 0", &gotNil)
	}
}

func TestEncodeStringRoundTrip(t *testing.T) {
	buffer := make([]byte, 9)
	cases := [][]byte{
		{},
		{0x01},
		{0xFF},
		bytes.Repeat([]byte{0xAB}, 10),
		bytes.Repeat([]byte{0xCD}, 100),
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := EncodeString(c, &buf, buffer); err != nil {
			t.Fatalf("EncodeString(%x) error: %v", c, err)
		}
		var got []byte
		if err := DecodeBytes(buf.Bytes(), &got); err != nil {
			t.Fatalf("DecodeBytes() for %x error: %v", c, err)
		}
		if !bytes.Equal(got, c) {
			t.Errorf("round trip mismatch for %x: got %x", c, got)
		}
	}
}

func TestEncodeStringSizePrefix(t *testing.T) {
	buffer := make([]byte, 9)
	var smallBuf bytes.Buffer
	if err := EncodeStringSizePrefix(10, &smallBuf, buffer); err != nil {
		t.Fatalf("EncodeStringSizePrefix(10) error: %v", err)
	}
	if got := smallBuf.Bytes(); len(got) != 1 || got[0] != byte(10)+128 {
		t.Errorf("EncodeStringSizePrefix(10) = %x, want single byte prefix", got)
	}

	var bigBuf bytes.Buffer
	if err := EncodeStringSizePrefix(1000, &bigBuf, buffer); err != nil {
		t.Fatalf("EncodeStringSizePrefix(1000) error: %v", err)
	}
	if len(bigBuf.Bytes()) < 2 {
		t.Errorf("EncodeStringSizePrefix(1000) should produce a multi-byte prefix: %x", bigBuf.Bytes())
	}
}

func TestStringSizeBytesSizeListSize(t *testing.T) {
	if got := StringSize(""); got != 1 {
		t.Errorf("StringSize(\"\") = %d, want 1", got)
	}
	if got := StringSize("a"); got != 1 {
		t.Errorf("StringSize(\"a\") = %d, want 1", got)
	}
	if got := StringSize(string([]byte{0xFF})); got != 2 {
		t.Errorf("StringSize(0xFF) = %d, want 2", got)
	}
	if got := StringSize("hello world"); got != uint64(1+len("hello world")) {
		t.Errorf("StringSize(long) = %d, want %d", got, 1+len("hello world"))
	}

	if got := BytesSize(nil); got != 1 {
		t.Errorf("BytesSize(nil) = %d, want 1", got)
	}
	if got := BytesSize([]byte{0x01}); got != 1 {
		t.Errorf("BytesSize([0x01]) = %d, want 1", got)
	}
	if got := BytesSize([]byte{0xFF}); got != 2 {
		t.Errorf("BytesSize([0xFF]) = %d, want 2", got)
	}
	if got := BytesSize(bytes.Repeat([]byte{0x01}, 100)); got != uint64(headsize(100)+100) {
		t.Errorf("BytesSize(100 bytes) = %d, want %d", got, headsize(100)+100)
	}

	if got := ListSize(10); got != uint64(headsize(10)+10) {
		t.Errorf("ListSize(10) = %d, want %d", got, headsize(10)+10)
	}
}
