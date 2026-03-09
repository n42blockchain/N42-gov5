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

package abi

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// abiTestTypes returns a set of ABI types for fuzz testing, along with
// their string names. Each type is constructed once and reused across
// fuzz iterations for efficiency.
func abiTestTypes() []struct {
	name string
	typ  Type
} {
	uint256Ty, _ := NewType("uint256", "", nil)
	addressTy, _ := NewType("address", "", nil)
	bytes32Ty, _ := NewType("bytes32", "", nil)
	stringTy, _ := NewType("string", "", nil)
	boolTy, _ := NewType("bool", "", nil)
	bytesTy, _ := NewType("bytes", "", nil)
	uint8Ty, _ := NewType("uint8", "", nil)
	int256Ty, _ := NewType("int256", "", nil)

	return []struct {
		name string
		typ  Type
	}{
		{"uint256", uint256Ty},
		{"address", addressTy},
		{"bytes32", bytes32Ty},
		{"string", stringTy},
		{"bool", boolTy},
		{"bytes", bytesTy},
		{"uint8", uint8Ty},
		{"int256", int256Ty},
	}
}

// FuzzABIUnpackArbitrary feeds random bytes to each known ABI type's unpacker,
// ensuring no panics occur. Errors are expected and acceptable.
func FuzzABIUnpackArbitrary(f *testing.F) {
	// Seed corpus: valid 32-byte ABI-encoded words and edge cases
	f.Add([]byte{})
	f.Add(make([]byte, 32))       // all zeros (32 bytes)
	f.Add(make([]byte, 64))       // 64 bytes
	f.Add(make([]byte, 31))       // too short for a word
	f.Add(make([]byte, 33))       // just over one word

	// A valid uint256 = 1
	word := make([]byte, 32)
	word[31] = 1
	f.Add(word)

	// A valid address (20 bytes right-aligned in 32)
	addrWord := make([]byte, 32)
	addrWord[31] = 0x01
	addrWord[30] = 0x02
	f.Add(addrWord)

	// A valid bool = true
	boolWord := make([]byte, 32)
	boolWord[31] = 1
	f.Add(boolWord)

	// A valid dynamic bytes: offset(32) + length(32) + data(32)
	dynBytes := make([]byte, 96)
	dynBytes[31] = 0x20 // offset = 32
	dynBytes[63] = 0x05 // length = 5
	copy(dynBytes[64:], []byte("hello"))
	f.Add(dynBytes)

	f.Fuzz(func(t *testing.T, data []byte) {
		testTypes := abiTestTypes()
		for _, tt := range testTypes {
			args := Arguments{{Name: "v", Type: tt.typ}}
			// Unpack should not panic on any input
			_, _ = args.Unpack(data)
		}
	})
}

// FuzzABIPackUnpackRoundtrip packs a uint256 value and unpacks it back,
// verifying the roundtrip produces identical results.
func FuzzABIPackUnpackRoundtrip(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{1})
	f.Add([]byte{0xff})
	f.Add([]byte{0x01, 0x00})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	// 32-byte max uint256
	max256 := make([]byte, 32)
	for i := range max256 {
		max256[i] = 0xff
	}
	f.Add(max256)

	uint256Ty, err := NewType("uint256", "", nil)
	if err != nil {
		f.Fatal(err)
	}
	args := Arguments{{Name: "v", Type: uint256Ty}}

	f.Fuzz(func(t *testing.T, valBytes []byte) {
		// Limit input size to 32 bytes (uint256 max)
		if len(valBytes) > 32 || len(valBytes) == 0 {
			return
		}

		val := new(big.Int).SetBytes(valBytes)

		packed, err := args.Pack(val)
		if err != nil {
			t.Fatalf("failed to pack uint256: %v", err)
		}

		unpacked, err := args.Unpack(packed)
		if err != nil {
			t.Fatalf("failed to unpack uint256: %v", err)
		}

		if len(unpacked) != 1 {
			t.Fatalf("expected 1 result, got %d", len(unpacked))
		}

		result, ok := unpacked[0].(*big.Int)
		if !ok {
			t.Fatalf("expected *big.Int, got %T", unpacked[0])
		}

		if val.Cmp(result) != 0 {
			t.Fatalf("roundtrip mismatch: original=%s, decoded=%s", val.String(), result.String())
		}
	})
}

// FuzzABIAddressPackUnpackRoundtrip packs an address and unpacks it back,
// verifying the roundtrip is identity.
func FuzzABIAddressPackUnpackRoundtrip(f *testing.F) {
	f.Add(make([]byte, 20))
	f.Add([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
		0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14})
	allFF := make([]byte, 20)
	for i := range allFF {
		allFF[i] = 0xff
	}
	f.Add(allFF)

	addrTy, err := NewType("address", "", nil)
	if err != nil {
		f.Fatal(err)
	}
	args := Arguments{{Name: "v", Type: addrTy}}

	f.Fuzz(func(t *testing.T, addrBytes []byte) {
		if len(addrBytes) != 20 {
			return
		}
		addr := types.BytesToAddress(addrBytes)

		packed, err := args.Pack(addr)
		if err != nil {
			t.Fatalf("failed to pack address: %v", err)
		}

		unpacked, err := args.Unpack(packed)
		if err != nil {
			t.Fatalf("failed to unpack address: %v", err)
		}

		if len(unpacked) != 1 {
			t.Fatalf("expected 1 result, got %d", len(unpacked))
		}

		result, ok := unpacked[0].(types.Address)
		if !ok {
			t.Fatalf("expected types.Address, got %T", unpacked[0])
		}

		if addr != result {
			t.Fatalf("roundtrip mismatch: original=%x, decoded=%x", addr, result)
		}
	})
}

// FuzzABIBoolPackUnpackRoundtrip packs a bool and unpacks it back,
// verifying the roundtrip is identity.
func FuzzABIBoolPackUnpackRoundtrip(f *testing.F) {
	f.Add(true)
	f.Add(false)

	boolTy, err := NewType("bool", "", nil)
	if err != nil {
		f.Fatal(err)
	}
	args := Arguments{{Name: "v", Type: boolTy}}

	f.Fuzz(func(t *testing.T, val bool) {
		packed, err := args.Pack(val)
		if err != nil {
			t.Fatalf("failed to pack bool: %v", err)
		}

		unpacked, err := args.Unpack(packed)
		if err != nil {
			t.Fatalf("failed to unpack bool: %v", err)
		}

		if len(unpacked) != 1 {
			t.Fatalf("expected 1 result, got %d", len(unpacked))
		}

		result, ok := unpacked[0].(bool)
		if !ok {
			t.Fatalf("expected bool, got %T", unpacked[0])
		}

		if val != result {
			t.Fatalf("roundtrip mismatch: original=%v, decoded=%v", val, result)
		}
	})
}

// FuzzABIStringPackUnpackRoundtrip packs a string and unpacks it back,
// verifying the roundtrip is identity.
func FuzzABIStringPackUnpackRoundtrip(f *testing.F) {
	f.Add("")
	f.Add("hello")
	f.Add("a]b[c")
	f.Add("0123456789abcdef0123456789abcdef") // 32 chars

	stringTy, err := NewType("string", "", nil)
	if err != nil {
		f.Fatal(err)
	}
	args := Arguments{{Name: "v", Type: stringTy}}

	f.Fuzz(func(t *testing.T, val string) {
		if len(val) > 4096 {
			return
		}

		packed, err := args.Pack(val)
		if err != nil {
			t.Fatalf("failed to pack string: %v", err)
		}

		unpacked, err := args.Unpack(packed)
		if err != nil {
			t.Fatalf("failed to unpack string: %v", err)
		}

		if len(unpacked) != 1 {
			t.Fatalf("expected 1 result, got %d", len(unpacked))
		}

		result, ok := unpacked[0].(string)
		if !ok {
			t.Fatalf("expected string, got %T", unpacked[0])
		}

		if val != result {
			t.Fatalf("roundtrip mismatch: original=%q, decoded=%q", val, result)
		}
	})
}
