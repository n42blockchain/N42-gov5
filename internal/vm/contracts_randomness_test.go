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

package vm

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

func TestRandomnessPrecompileAddress(t *testing.T) {
	expected := types.HexToAddress("0x0000000000000000000000000000000000000302")
	if RandomnessAddress != expected {
		t.Fatalf("RandomnessAddress = %s, want %s", RandomnessAddress.Hex(), expected.Hex())
	}

	pc, ok := PrecompiledContractsRandomness[RandomnessAddress]
	if !ok {
		t.Fatal("RandomnessAddress not found in PrecompiledContractsRandomness map")
	}
	if pc == nil {
		t.Fatal("precompile contract at RandomnessAddress is nil")
	}
}

func TestRandomnessGetRandom(t *testing.T) {
	// Set a known randomness value.
	r := crypto.Keccak256Hash([]byte("test-randomness"))
	SetBlockRandomness(1, r)
	defer SetBlockRandomness(0, types.Hash{})

	c := &randomnessBeacon{}
	input := []byte{rngGetRandom}
	out, err := c.Run(input)
	if err != nil {
		t.Fatalf("getRandom failed: %v", err)
	}
	if len(out) != 32 {
		t.Fatalf("getRandom output length = %d, want 32", len(out))
	}

	// Verify output is keccak256(r).
	expected := crypto.Keccak256Hash(r[:])
	if !bytes.Equal(out, expected[:]) {
		t.Fatalf("getRandom output mismatch: got %x, want %x", out, expected[:])
	}
}

func TestRandomnessGetRandomInRange(t *testing.T) {
	r := crypto.Keccak256Hash([]byte("range-test"))
	SetBlockRandomness(1, r)
	defer SetBlockRandomness(0, types.Hash{})

	c := &randomnessBeacon{}

	// max = 100
	max := big.NewInt(100)
	maxBytes := make([]byte, 32)
	maxB := max.Bytes()
	copy(maxBytes[32-len(maxB):], maxB)

	input := append([]byte{rngGetRandomInRange}, maxBytes...)
	out, err := c.Run(input)
	if err != nil {
		t.Fatalf("getRandomInRange failed: %v", err)
	}
	if len(out) != 32 {
		t.Fatalf("getRandomInRange output length = %d, want 32", len(out))
	}

	result := new(big.Int).SetBytes(out)
	if result.Cmp(max) >= 0 {
		t.Fatalf("result %d >= max %d", result, max)
	}
	if result.Sign() < 0 {
		t.Fatalf("result is negative: %d", result)
	}
}

func TestRandomnessGetRandomInRangeZeroMax(t *testing.T) {
	c := &randomnessBeacon{}

	maxBytes := make([]byte, 32) // all zeros = max of 0
	input := append([]byte{rngGetRandomInRange}, maxBytes...)
	_, err := c.Run(input)
	if err == nil {
		t.Fatal("expected error for max=0")
	}
	if !errors.Is(err, errRandomnessZeroMax) {
		t.Fatalf("expected errRandomnessZeroMax, got: %v", err)
	}
}

func TestRandomnessGetRandomWithSeed(t *testing.T) {
	r := crypto.Keccak256Hash([]byte("seed-test"))
	SetBlockRandomness(1, r)
	defer SetBlockRandomness(0, types.Hash{})

	c := &randomnessBeacon{}

	seed1 := crypto.Keccak256Hash([]byte("seed-1"))
	seed2 := crypto.Keccak256Hash([]byte("seed-2"))

	input1 := append([]byte{rngGetRandomWithSeed}, seed1[:]...)
	input2 := append([]byte{rngGetRandomWithSeed}, seed2[:]...)

	out1, err := c.Run(input1)
	if err != nil {
		t.Fatalf("getRandomWithSeed(seed1) failed: %v", err)
	}
	out2, err := c.Run(input2)
	if err != nil {
		t.Fatalf("getRandomWithSeed(seed2) failed: %v", err)
	}

	if len(out1) != 32 || len(out2) != 32 {
		t.Fatal("output length mismatch")
	}

	// Different seeds should produce different outputs.
	if bytes.Equal(out1, out2) {
		t.Fatal("different seeds produced same output")
	}

	// Verify determinism: same seed gives same output.
	out1b, _ := c.Run(input1)
	if !bytes.Equal(out1, out1b) {
		t.Fatal("same seed produced different outputs on second call")
	}

	// Verify output matches expected value: keccak256(r || seed1).
	combined := make([]byte, 64)
	copy(combined[:32], r[:])
	copy(combined[32:], seed1[:])
	expected := crypto.Keccak256Hash(combined)
	if !bytes.Equal(out1, expected[:]) {
		t.Fatalf("getRandomWithSeed output mismatch: got %x, want %x", out1, expected[:])
	}
}

func TestRandomnessGas(t *testing.T) {
	c := &randomnessBeacon{}

	tests := []struct {
		name  string
		input []byte
		want  uint64
	}{
		{
			name:  "empty input",
			input: []byte{},
			want:  RandomnessGetRandomGas,
		},
		{
			name:  "getRandom",
			input: []byte{rngGetRandom},
			want:  RandomnessGetRandomGas,
		},
		{
			name:  "getRandomInRange",
			input: []byte{rngGetRandomInRange},
			want:  RandomnessGetRandomInRangeGas,
		},
		{
			name:  "getRandomWithSeed",
			input: []byte{rngGetRandomWithSeed},
			want:  RandomnessGetRandomGas,
		},
		{
			name:  "unknown selector",
			input: []byte{0xFF},
			want:  RandomnessGetRandomGas,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.RequiredGas(tc.input)
			if got != tc.want {
				t.Errorf("RequiredGas() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRandomnessNoBackend(t *testing.T) {
	// With zero randomness (default), getRandom should return keccak256(zeroHash).
	SetBlockRandomness(0, types.Hash{})
	c := &randomnessBeacon{}

	out, err := c.Run([]byte{rngGetRandom})
	if err != nil {
		t.Fatalf("getRandom with zero randomness failed: %v", err)
	}

	zeroHash := types.Hash{}
	expected := crypto.Keccak256Hash(zeroHash[:])
	if !bytes.Equal(out, expected[:]) {
		t.Fatalf("zero randomness output mismatch: got %x, want %x", out, expected[:])
	}
}
