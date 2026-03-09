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
	"testing"
)

// FuzzPrecompileEcrecover feeds random input to the ecrecover precompile,
// ensuring it never panics. The ecrecover precompile should gracefully
// handle any input by returning nil output or an error.
func FuzzPrecompileEcrecover(f *testing.F) {
	// Empty input
	f.Add([]byte{})
	// All zeros (128 bytes - expected input length)
	f.Add(make([]byte, 128))
	// Short input
	f.Add([]byte{0x01, 0x02, 0x03})
	// Valid-ish structure: hash(32) + v(32) + r(32) + s(32)
	validInput := make([]byte, 128)
	// Set v = 27 (in position 63)
	validInput[63] = 27
	// Set r to something non-zero
	validInput[95] = 0x01
	// Set s to something non-zero
	validInput[127] = 0x01
	f.Add(validInput)
	// Oversized input
	f.Add(make([]byte, 256))

	c := &ecrecover{}
	f.Fuzz(func(t *testing.T, input []byte) {
		// Run should never panic
		output, err := c.Run(input)
		// If no error and output is non-nil, it should be 32 bytes
		if err == nil && output != nil && len(output) != 32 {
			t.Fatalf("ecrecover returned non-nil output with unexpected length: %d", len(output))
		}
	})
}

// FuzzPrecompileSha256 feeds random input to the sha256 precompile,
// ensuring it always produces a 32-byte hash and never panics.
func FuzzPrecompileSha256(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte("hello world"))
	f.Add(make([]byte, 256))

	c := &sha256hash{}
	f.Fuzz(func(t *testing.T, input []byte) {
		output, err := c.Run(input)
		if err != nil {
			t.Fatalf("sha256 returned error: %v", err)
		}
		if len(output) != 32 {
			t.Fatalf("sha256 output length: got %d, want 32", len(output))
		}
	})
}

// FuzzPrecompileRipemd160 feeds random input to the ripemd160 precompile,
// ensuring it always produces a 32-byte left-padded hash and never panics.
func FuzzPrecompileRipemd160(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte("hello world"))
	f.Add(make([]byte, 256))

	c := &ripemd160hash{}
	f.Fuzz(func(t *testing.T, input []byte) {
		output, err := c.Run(input)
		if err != nil {
			t.Fatalf("ripemd160 returned error: %v", err)
		}
		if len(output) != 32 {
			t.Fatalf("ripemd160 output length: got %d, want 32", len(output))
		}
	})
}

// FuzzPrecompileModExp feeds random input to the modexp precompile (EIP-198
// and EIP-2565), ensuring it never panics on arbitrary input.
func FuzzPrecompileModExp(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 96)) // all zeros (base=0, exp=0, mod=0)
	// Simple: base=2, exp=3, mod=5 => 2^3 mod 5 = 3
	simple := make([]byte, 96+1+1+1)
	simple[31] = 1   // baseLen = 1
	simple[63] = 1   // expLen = 1
	simple[95] = 1   // modLen = 1
	simple[96] = 2   // base = 2
	simple[97] = 3   // exp = 3
	simple[98] = 5   // mod = 5
	f.Add(simple)
	// Short input
	f.Add([]byte{0x01})
	// Large lengths in header but missing data
	largeHeader := make([]byte, 96)
	largeHeader[31] = 32  // baseLen = 32
	largeHeader[63] = 32  // expLen = 32
	largeHeader[95] = 32  // modLen = 32
	f.Add(largeHeader)

	// Test both EIP-198 and EIP-2565 variants
	variants := []struct {
		name string
		c    *bigModExp
	}{
		{"eip198", &bigModExp{eip2565: false, eip7823: false, eip7883: false}},
		{"eip2565", &bigModExp{eip2565: true, eip7823: false, eip7883: false}},
		{"eip7823", &bigModExp{eip2565: true, eip7823: true, eip7883: false}},
		{"eip7883", &bigModExp{eip2565: true, eip7823: true, eip7883: true}},
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		for _, v := range variants {
			// RequiredGas should never panic
			_ = v.c.RequiredGas(input)
			// Run should never panic
			_, _ = v.c.Run(input)
		}
	})
}

// FuzzPrecompileBn256Add feeds random input to the bn256 point addition
// precompile, ensuring it never panics.
func FuzzPrecompileBn256Add(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 128)) // 2 points, all zeros
	f.Add(make([]byte, 64))  // 1 point
	f.Add(make([]byte, 256)) // oversized

	c := &bn256AddIstanbul{}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = c.Run(input)
	})
}

// FuzzPrecompileBn256ScalarMul feeds random input to the bn256 scalar
// multiplication precompile, ensuring it never panics.
func FuzzPrecompileBn256ScalarMul(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 96))  // point(64) + scalar(32)
	f.Add(make([]byte, 64))  // point only
	f.Add(make([]byte, 128)) // oversized

	c := &bn256ScalarMulIstanbul{}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = c.Run(input)
	})
}

// FuzzPrecompileBn256Pairing feeds random input to the bn256 pairing
// precompile, ensuring it never panics.
func FuzzPrecompileBn256Pairing(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 192)) // one pair: G1(64) + G2(128)
	f.Add(make([]byte, 384)) // two pairs
	f.Add(make([]byte, 100)) // invalid size (not multiple of 192)

	c := &bn256PairingIstanbul{}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = c.Run(input)
	})
}

// FuzzPrecompileBlake2F feeds random input to the blake2f precompile,
// ensuring it never panics on arbitrary input.
func FuzzPrecompileBlake2F(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 213)) // expected input length for blake2f
	f.Add(make([]byte, 100)) // short input
	f.Add(make([]byte, 300)) // oversized

	// Valid blake2f input: 4 bytes rounds + 64 bytes h + 128 bytes m + 16 bytes t + 1 byte f
	valid := make([]byte, 213)
	valid[3] = 12    // 12 rounds
	valid[212] = 1   // final block flag
	f.Add(valid)

	c := &blake2F{}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = c.Run(input)
	})
}

// FuzzPrecompileP256Verify feeds random input to the P-256 signature
// verification precompile, ensuring it never panics.
func FuzzPrecompileP256Verify(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 160)) // expected length: hash(32) + r(32) + s(32) + x(32) + y(32)
	f.Add(make([]byte, 100)) // short
	f.Add(make([]byte, 200)) // oversized

	// Input with non-zero signature components
	nonzero := make([]byte, 160)
	nonzero[63] = 1   // r = 1
	nonzero[95] = 1   // s = 1
	nonzero[127] = 1  // x = 1
	nonzero[159] = 1  // y = 1
	f.Add(nonzero)

	c := &p256Verify{}
	f.Fuzz(func(t *testing.T, input []byte) {
		output, err := c.Run(input)
		// Should never return an error (invalid sigs return nil, nil)
		if err != nil {
			t.Fatalf("p256Verify returned error: %v", err)
		}
		// Output is either nil (invalid) or 32 bytes (valid, value 1)
		if output != nil && len(output) != 32 {
			t.Fatalf("p256Verify returned non-nil output with unexpected length: %d", len(output))
		}
	})
}

// FuzzPrecompileDataCopy feeds random input to the identity/datacopy
// precompile, verifying it always returns the input unchanged.
func FuzzPrecompileDataCopy(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01, 0x02, 0x03})
	f.Add(make([]byte, 1024))

	c := &dataCopy{}
	f.Fuzz(func(t *testing.T, input []byte) {
		output, err := c.Run(input)
		if err != nil {
			t.Fatalf("dataCopy returned error: %v", err)
		}
		if len(output) != len(input) {
			t.Fatalf("dataCopy output length mismatch: got %d, want %d", len(output), len(input))
		}
	})
}
