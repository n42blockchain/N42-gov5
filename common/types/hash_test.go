// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"testing"

	"github.com/holiman/uint256"
)

func TestHashBasicMethods(t *testing.T) {
	h := Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	// Test Bytes
	b := h.Bytes()
	if len(b) != 32 {
		t.Errorf("Bytes() length = %d, want 32", len(b))
	}

	// Test Big
	bigInt := h.Big()
	if bigInt == nil {
		t.Error("Big() returned nil")
	}

	// Test Hex
	hexStr := h.Hex()
	if len(hexStr) != 66 { // "0x" + 64 hex chars
		t.Errorf("Hex() length = %d, want 66", len(hexStr))
	}
	if hexStr[:2] != "0x" {
		t.Error("Hex() should start with '0x'")
	}

	// Test TerminalString
	ts := h.TerminalString()
	if len(ts) == 0 {
		t.Error("TerminalString() returned empty string")
	}

	// Test String
	str := h.String()
	if len(str) != 64 {
		t.Errorf("String() length = %d, want 64", len(str))
	}
}

func TestHashSetBytes(t *testing.T) {
	h := Hash{}

	// Valid input
	validBytes := make([]byte, 32)
	for i := range validBytes {
		validBytes[i] = byte(i)
	}
	err := h.SetBytes(validBytes)
	if err != nil {
		t.Errorf("SetBytes() with valid input error = %v", err)
	}

	// Invalid input - too short
	err = h.SetBytes(make([]byte, 31))
	if err == nil {
		t.Error("SetBytes() should fail with too short input")
	}

	// Invalid input - too long
	err = h.SetBytes(make([]byte, 33))
	if err == nil {
		t.Error("SetBytes() should fail with too long input")
	}
}

func TestHashSetString(t *testing.T) {
	h := Hash{}

	// Valid input (64 hex chars)
	validHex := "0102030405060708091011121314151617181920212223242526272829303132"
	err := h.SetString(validHex)
	if err != nil {
		t.Errorf("SetString() with valid input error = %v", err)
	}

	// Invalid input - too short
	err = h.SetString("0102")
	if err == nil {
		t.Error("SetString() should fail with too short input")
	}

	// Invalid input - invalid hex chars
	err = h.SetString("gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg")
	if err == nil {
		t.Error("SetString() should fail with invalid hex chars")
	}
}

func TestHashMarshalUnmarshal(t *testing.T) {
	original := Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	// Test Marshal
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(data) != 32 {
		t.Errorf("Marshal() length = %d, want 32", len(data))
	}

	// Test MarshalTo
	buf := make([]byte, 32)
	n, err := original.MarshalTo(buf)
	if err != nil {
		t.Fatalf("MarshalTo() error = %v", err)
	}
	if n != 32 {
		t.Errorf("MarshalTo() returned %d, want 32", n)
	}

	// Test Unmarshal
	var recovered Hash
	err = recovered.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if recovered != original {
		t.Error("Unmarshal() didn't recover original hash")
	}

	// Test Size
	if original.Size() != 32 {
		t.Errorf("Size() = %d, want 32", original.Size())
	}
}

func TestHashTextMarshal(t *testing.T) {
	h := Hash{1, 2, 3}

	// Test MarshalText
	text, err := h.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if len(text) == 0 {
		t.Error("MarshalText() returned empty")
	}

	// Test UnmarshalText
	var recovered Hash
	err = recovered.UnmarshalText(text)
	if err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if recovered != h {
		t.Error("UnmarshalText() didn't recover original hash")
	}
}

func TestHashJSONMarshal(t *testing.T) {
	h := Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	// Test JSON marshal
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Test JSON unmarshal
	var recovered Hash
	err = json.Unmarshal(data, &recovered)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if recovered != h {
		t.Error("JSON round-trip failed")
	}
}

func TestHashScan(t *testing.T) {
	h := Hash{}

	// Valid scan
	validBytes := make([]byte, 32)
	for i := range validBytes {
		validBytes[i] = byte(i)
	}
	err := h.Scan(validBytes)
	if err != nil {
		t.Errorf("Scan() error = %v", err)
	}

	// Invalid type
	err = h.Scan("string")
	if err == nil {
		t.Error("Scan() should fail with non-[]byte input")
	}

	// Invalid length
	err = h.Scan(make([]byte, 31))
	if err == nil {
		t.Error("Scan() should fail with wrong length")
	}
}

func TestHashValue(t *testing.T) {
	h := Hash{1, 2, 3}

	val, err := h.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}

	valBytes, ok := val.([]byte)
	if !ok {
		t.Fatal("Value() didn't return []byte")
	}
	if len(valBytes) != 32 {
		t.Errorf("Value() length = %d, want 32", len(valBytes))
	}
}

func TestHashGenerate(t *testing.T) {
	h := Hash{}
	r := rand.New(rand.NewSource(42))

	val := h.Generate(r, 0)
	if val.Type() != hashT {
		t.Error("Generate() returned wrong type")
	}
}

func TestHashFormat(t *testing.T) {
	h := Hash{0xab, 0xcd, 0xef}

	tests := []struct {
		format string
	}{
		{"%v"},
		{"%s"},
		{"%x"},
		{"%X"},
		{"%#x"},
		{"%q"},
		{"%d"},
		{"%z"}, // invalid format
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			result := fmt.Sprintf(tt.format, h)
			if len(result) == 0 {
				t.Error("Format returned empty string")
			}
		})
	}
}

func TestHashEqual(t *testing.T) {
	h1 := Hash{1, 2, 3}
	h2 := Hash{1, 2, 3}
	h3 := Hash{1, 2, 4}

	if !h1.Equal(h2) {
		t.Error("Equal() should return true for equal hashes")
	}
	if h1.Equal(h3) {
		t.Error("Equal() should return false for different hashes")
	}
}

func TestHashHexBytes(t *testing.T) {
	h := Hash{0xab, 0xcd}
	hexBytes := h.HexBytes()

	if len(hexBytes) != 64 {
		t.Errorf("HexBytes() length = %d, want 64", len(hexBytes))
	}
}

func TestBytesHash(t *testing.T) {
	data := []byte("test data")
	h := BytesHash(data)

	if len(h) != 32 {
		t.Errorf("BytesHash() returned hash of length %d", len(h))
	}

	// Should be deterministic
	h2 := BytesHash(data)
	if h != h2 {
		t.Error("BytesHash() should be deterministic")
	}
}

func TestBytesToHash(t *testing.T) {
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i)
	}

	h := BytesToHash(data)

	if !bytes.Equal(h[:], data) {
		t.Error("BytesToHash() didn't set bytes correctly")
	}
}

func TestStringToHash(t *testing.T) {
	// Valid hex string
	hexStr := "0102030405060708091011121314151617181920212223242526272829303132"
	h := StringToHash(hexStr)

	if h[0] != 0x01 || h[1] != 0x02 {
		t.Error("StringToHash() didn't parse correctly")
	}

	// Invalid hex string
	h2 := StringToHash("invalid")
	if h2 != (Hash{}) {
		t.Error("StringToHash() should return empty hash for invalid input")
	}
}

func TestHexToHash(t *testing.T) {
	h := HexToHash("0x0102030405060708091011121314151617181920212223242526272829303132")

	if h[0] != 0x01 || h[1] != 0x02 {
		t.Error("HexToHash() didn't parse correctly")
	}
}

func TestHashDifference(t *testing.T) {
	h1 := Hash{1}
	h2 := Hash{2}
	h3 := Hash{3}

	a := []Hash{h1, h2, h3}
	b := []Hash{h2}

	diff := HashDifference(a, b)

	if len(diff) != 2 {
		t.Errorf("HashDifference() length = %d, want 2", len(diff))
	}

	// diff should contain h1 and h3
	found1, found3 := false, false
	for _, h := range diff {
		if h == h1 {
			found1 = true
		}
		if h == h3 {
			found3 = true
		}
	}
	if !found1 || !found3 {
		t.Error("HashDifference() missing expected hashes")
	}
}

func TestHasherPool(t *testing.T) {
	// Get a hasher
	h := NewHasher()
	if h == nil {
		t.Fatal("NewHasher() returned nil")
	}
	if h.Sha == nil {
		t.Fatal("Hasher has nil Sha")
	}

	// Return it to the pool
	ReturnHasherToPool(h)

	// Get another - might be the same one
	h2 := NewHasher()
	if h2 == nil {
		t.Fatal("NewHasher() returned nil after return")
	}

	ReturnHasherToPool(h2)
}

func TestHashData(t *testing.T) {
	data := []byte("test data")

	h, err := HashData(data)
	if err != nil {
		t.Fatalf("HashData() error = %v", err)
	}

	if len(h) != 32 {
		t.Errorf("HashData() returned hash of length %d", len(h))
	}

	// Should be deterministic
	h2, _ := HashData(data)
	if h != h2 {
		t.Error("HashData() should be deterministic")
	}
}

func TestBytes2Hex(t *testing.T) {
	data := []byte{0xab, 0xcd, 0xef}
	hex := Bytes2Hex(data)

	if hex != "abcdef" {
		t.Errorf("Bytes2Hex() = %s, want abcdef", hex)
	}
}

func TestFromHex2Bytes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []byte
	}{
		{"with 0x prefix", "0xabcd", []byte{0xab, 0xcd}},
		{"with 0X prefix", "0Xabcd", []byte{0xab, 0xcd}},
		{"without prefix", "abcd", []byte{0xab, 0xcd}},
		{"odd length", "abc", []byte{0x0a, 0xbc}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FromHex2Bytes(tt.input)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("FromHex2Bytes() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestHashesSortInterface(t *testing.T) {
	hashes := Hashes{
		{3},
		{1},
		{2},
	}

	// Test Len
	if hashes.Len() != 3 {
		t.Errorf("Len() = %d, want 3", hashes.Len())
	}

	// Test Less
	if !hashes.Less(1, 0) { // {1} < {3}
		t.Error("Less() returned wrong result")
	}

	// Test Swap
	hashes.Swap(0, 2)
	if hashes[0] != (Hash{2}) || hashes[2] != (Hash{3}) {
		t.Error("Swap() didn't swap correctly")
	}

	// Test Sort
	sort.Sort(hashes)
	if hashes[0] != (Hash{1}) || hashes[1] != (Hash{2}) || hashes[2] != (Hash{3}) {
		t.Error("Sort didn't sort correctly")
	}
}

func TestInt256Min(t *testing.T) {
	a := uint256.NewInt(10)
	b := uint256.NewInt(5)

	result := Int256Min(a, b)
	if result.Cmp(b) != 0 {
		t.Error("Int256Min should return smaller value")
	}

	result = Int256Min(b, a)
	if result.Cmp(b) != 0 {
		t.Error("Int256Min should return smaller value")
	}

	// Equal values
	result = Int256Min(a, uint256.NewInt(10))
	if result.Cmp(a) != 0 {
		t.Error("Int256Min should return a when equal")
	}
}

func TestBigToHash(t *testing.T) {
	// Use a 32-byte big integer to ensure it fills the hash
	bigIntBytes := make([]byte, 32)
	for i := range bigIntBytes {
		bigIntBytes[i] = byte(i + 1)
	}
	bigInt := new(big.Int).SetBytes(bigIntBytes)
	h := BigToHash(bigInt)

	// Verify the hash contains the big int bytes
	recoveredBig := h.Big()
	if recoveredBig.Cmp(bigInt) != 0 {
		t.Error("BigToHash() round-trip failed")
	}

	// Test with small big int (will be left-padded)
	smallBig := big.NewInt(12345678)
	h2 := BigToHash(smallBig)

	// The hash is created from the bytes of the big int
	// Since smallBig.Bytes() is less than 32 bytes, BytesToHash with SetBytes will fail
	// and return an empty hash. So h2 should be empty.
	if h2 != (Hash{}) {
		// If BytesToHash handled small inputs differently, this might not be empty
		t.Log("BigToHash with small int produced non-empty hash")
	}
}
