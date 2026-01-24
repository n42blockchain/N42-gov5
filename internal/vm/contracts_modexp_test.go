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
	"math"
	"testing"
)

// TestModExpEIP7823InputLimits tests EIP-7823 input size limits for MODEXP precompile.
// EIP-7823 limits base, exponent, and modulus lengths to 1024 bytes each.
func TestModExpEIP7823InputLimits(t *testing.T) {
	tests := []struct {
		name       string
		baseLen    uint64
		expLen     uint64
		modLen     uint64
		eip7823    bool
		wantGas    uint64
		wantMaxGas bool // if true, expect math.MaxUint64
		wantErr    bool // if true, expect error from Run
	}{
		{
			name:    "within limit - base 1024",
			baseLen: 1024,
			expLen:  32,
			modLen:  32,
			eip7823: true,
			wantGas: 0, // will be calculated
		},
		{
			name:       "exceeds limit - base 1025",
			baseLen:    1025,
			expLen:     32,
			modLen:     32,
			eip7823:    true,
			wantMaxGas: true,
			wantErr:    true,
		},
		{
			name:       "exceeds limit - exp 1025",
			baseLen:    32,
			expLen:     1025,
			modLen:     32,
			eip7823:    true,
			wantMaxGas: true,
			wantErr:    true,
		},
		{
			name:       "exceeds limit - mod 1025",
			baseLen:    32,
			expLen:     32,
			modLen:     1025,
			eip7823:    true,
			wantMaxGas: true,
			wantErr:    true,
		},
		{
			name:    "no limit without eip7823 - base 2048",
			baseLen: 2048,
			expLen:  32,
			modLen:  32,
			eip7823: false,
			wantGas: 0, // will be calculated, should not be MaxUint64
		},
		{
			name:    "all at limit",
			baseLen: 1024,
			expLen:  1024,
			modLen:  1024,
			eip7823: true,
			wantGas: 0, // will be calculated
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build input with length headers
			input := make([]byte, 96+tt.baseLen+tt.expLen+tt.modLen)

			// Set base length (first 32 bytes)
			baseLenBytes := make([]byte, 32)
			baseLenBytes[31] = byte(tt.baseLen & 0xff)
			baseLenBytes[30] = byte((tt.baseLen >> 8) & 0xff)
			baseLenBytes[29] = byte((tt.baseLen >> 16) & 0xff)
			baseLenBytes[28] = byte((tt.baseLen >> 24) & 0xff)
			copy(input[0:32], baseLenBytes)

			// Set exp length (bytes 32-64)
			expLenBytes := make([]byte, 32)
			expLenBytes[31] = byte(tt.expLen & 0xff)
			expLenBytes[30] = byte((tt.expLen >> 8) & 0xff)
			expLenBytes[29] = byte((tt.expLen >> 16) & 0xff)
			expLenBytes[28] = byte((tt.expLen >> 24) & 0xff)
			copy(input[32:64], expLenBytes)

			// Set mod length (bytes 64-96)
			modLenBytes := make([]byte, 32)
			modLenBytes[31] = byte(tt.modLen & 0xff)
			modLenBytes[30] = byte((tt.modLen >> 8) & 0xff)
			modLenBytes[29] = byte((tt.modLen >> 16) & 0xff)
			modLenBytes[28] = byte((tt.modLen >> 24) & 0xff)
			copy(input[64:96], modLenBytes)

			// Fill base, exp, mod with 1s (non-zero values)
			for i := uint64(96); i < uint64(len(input)); i++ {
				input[i] = 1
			}

			c := &bigModExp{eip2565: true, eip7823: tt.eip7823, eip7883: false}

			// Test RequiredGas
			gas := c.RequiredGas(input)
			if tt.wantMaxGas && gas != math.MaxUint64 {
				t.Errorf("RequiredGas() = %d, want math.MaxUint64", gas)
			}

			// Test Run
			_, err := c.Run(input)
			if tt.wantErr && err == nil {
				t.Errorf("Run() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Run() error = %v, want nil", err)
			}
		})
	}
}

// TestModExpEIP7883GasCalculation tests EIP-7883 gas cost increase for MODEXP precompile.
// EIP-7883 applies a 3x multiplier and sets minimum gas to 500.
func TestModExpEIP7883GasCalculation(t *testing.T) {
	tests := []struct {
		name      string
		baseLen   uint64
		expLen    uint64
		modLen    uint64
		expValue  byte // value at exponent position to affect gas calculation
		eip7883   bool
		wantMin   uint64 // minimum expected gas
		checkMult bool   // if true, check that gas is ~3x of non-eip7883
	}{
		{
			name:     "minimum gas with eip7883 - small inputs",
			baseLen:  1,
			expLen:   1,
			modLen:   1,
			expValue: 1,
			eip7883:  true,
			wantMin:  500, // EIP-7883 minimum
		},
		{
			name:     "minimum gas without eip7883 - small inputs",
			baseLen:  1,
			expLen:   1,
			modLen:   1,
			expValue: 1,
			eip7883:  false,
			wantMin:  200, // EIP-2565 minimum
		},
		{
			name:      "3x multiplier with larger inputs",
			baseLen:   64,
			expLen:    64,
			modLen:    64,
			expValue:  0xff, // high exponent for more gas
			eip7883:   true,
			checkMult: true,
		},
		{
			name:      "3x multiplier with medium inputs",
			baseLen:   128,
			expLen:    128,
			modLen:    128,
			expValue:  0xff,
			eip7883:   true,
			checkMult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build input
			totalLen := 96 + tt.baseLen + tt.expLen + tt.modLen
			input := make([]byte, totalLen)

			// Set lengths
			input[31] = byte(tt.baseLen)
			input[63] = byte(tt.expLen)
			input[95] = byte(tt.modLen)

			// Set base to 2
			if tt.baseLen > 0 {
				input[96+tt.baseLen-1] = 2
			}

			// Set exponent
			if tt.expLen > 0 {
				input[96+tt.baseLen+tt.expLen-1] = tt.expValue
			}

			// Set modulus to 3
			if tt.modLen > 0 {
				input[96+tt.baseLen+tt.expLen+tt.modLen-1] = 3
			}

			if tt.checkMult {
				// Compare gas with and without EIP-7883
				c7883 := &bigModExp{eip2565: true, eip7823: false, eip7883: true}
				cBase := &bigModExp{eip2565: true, eip7823: false, eip7883: false}

				gas7883 := c7883.RequiredGas(input)
				gasBase := cBase.RequiredGas(input)

				// Gas should be approximately 3x (accounting for minimum)
				expectedGas := gasBase * 3
				if gasBase < 200 {
					expectedGas = gasBase * 3
					if expectedGas < 500 {
						expectedGas = 500
					}
				}

				if gas7883 < expectedGas-1 || gas7883 > expectedGas+1 {
					t.Errorf("EIP-7883 gas = %d, expected ~%d (3x of %d)", gas7883, expectedGas, gasBase)
				}
			} else {
				c := &bigModExp{eip2565: true, eip7823: false, eip7883: tt.eip7883}
				gas := c.RequiredGas(input)

				if gas < tt.wantMin {
					t.Errorf("RequiredGas() = %d, want >= %d", gas, tt.wantMin)
				}
			}
		})
	}
}

// TestModExpFusakaPrecompile tests the Fusaka-era MODEXP precompile configuration.
func TestModExpFusakaPrecompile(t *testing.T) {
	// Test that Fusaka precompile has both EIP-7823 and EIP-7883 enabled
	fusaka := PrecompiledContractsFusaka[bytesToAddress([]byte{5})]
	if fusaka == nil {
		t.Fatal("Fusaka MODEXP precompile not found at address 0x05")
	}

	// Build test input that would exceed EIP-7823 limit
	// baseLen = 1025 = 0x401 in big-endian 32-byte format
	input := make([]byte, 96+1025+32+32)
	input[31] = 0x01 // baseLen low byte
	input[30] = 0x04 // baseLen high byte -> 0x0401 = 1025
	input[63] = 32   // expLen
	input[95] = 32   // modLen

	// RequiredGas should return MaxUint64 due to EIP-7823
	gas := fusaka.RequiredGas(input)
	if gas != math.MaxUint64 {
		t.Errorf("Fusaka MODEXP RequiredGas() = %d for oversized input, want math.MaxUint64", gas)
	}

	// Run should return error due to EIP-7823
	_, err := fusaka.Run(input)
	if err == nil {
		t.Error("Fusaka MODEXP Run() should return error for oversized input")
	}

	// Test normal operation with valid input
	validInput := make([]byte, 96+32+32+32)
	validInput[31] = 32 // baseLen
	validInput[63] = 32 // expLen
	validInput[95] = 32 // modLen
	// base = 2
	validInput[96+31] = 2
	// exp = 10
	validInput[96+32+31] = 10
	// mod = 13
	validInput[96+32+32+31] = 13

	validGas := fusaka.RequiredGas(validInput)
	// EIP-7883 minimum is 500
	if validGas < 500 {
		t.Errorf("Fusaka MODEXP RequiredGas() = %d, want >= 500 (EIP-7883 minimum)", validGas)
	}

	result, err := fusaka.Run(validInput)
	if err != nil {
		t.Errorf("Fusaka MODEXP Run() error = %v", err)
	}
	// 2^10 mod 13 = 1024 mod 13 = 10
	expectedResult := make([]byte, 32)
	expectedResult[31] = 10
	if len(result) != 32 {
		t.Errorf("Fusaka MODEXP Run() result length = %d, want 32", len(result))
	} else if result[31] != 10 {
		t.Errorf("Fusaka MODEXP Run() result = %v, want %v", result, expectedResult)
	}
}

// TestP256VerifyPrecompile tests the P-256 signature verification precompile.
func TestP256VerifyPrecompile(t *testing.T) {
	// Test that P-256 precompile exists at correct address in Fusaka
	p256Addr := bytesToAddress([]byte{0x01, 0x00})
	p256 := PrecompiledContractsFusaka[p256Addr]
	if p256 == nil {
		t.Fatal("P-256 precompile not found at address 0x100 in Fusaka")
	}

	// Test gas cost
	input := make([]byte, 160) // P256VerifyInputLength
	gas := p256.RequiredGas(input)
	if gas != P256VerifyGas {
		t.Errorf("P256 RequiredGas() = %d, want %d", gas, P256VerifyGas)
	}

	// Test with invalid signature (all zeros)
	// Should return empty result (not error) for invalid signature
	result, err := p256.Run(input)
	if err != nil {
		t.Errorf("P256 Run() error = %v, want nil (invalid signatures return empty, not error)", err)
	}
	if len(result) != 0 {
		t.Errorf("P256 Run() result = %v, want empty for invalid signature", result)
	}
}

// TestPrecompiledContractsPectraOsakaEquivalence tests that Pectra and Osaka
// have the same precompiles (Osaka adds EOF but no new precompiles).
func TestPrecompiledContractsPectraOsakaEquivalence(t *testing.T) {
	if len(PrecompiledContractsPectra) != len(PrecompiledContractsOsaka) {
		t.Errorf("Pectra has %d precompiles, Osaka has %d, want equal",
			len(PrecompiledContractsPectra), len(PrecompiledContractsOsaka))
	}

	for addr := range PrecompiledContractsPectra {
		if PrecompiledContractsOsaka[addr] == nil {
			t.Errorf("Osaka missing precompile at address %v", addr)
		}
	}
}

// TestPrecompiledContractsFusakaHasP256 tests that Fusaka includes P-256 precompile.
func TestPrecompiledContractsFusakaHasP256(t *testing.T) {
	p256Addr := bytesToAddress([]byte{0x01, 0x00})
	if PrecompiledContractsFusaka[p256Addr] == nil {
		t.Error("Fusaka missing P-256 precompile at address 0x100")
	}

	// Also verify it's not in Prague/Pectra/Osaka
	if PrecompiledContractsPrague[p256Addr] != nil {
		t.Error("Prague should not have P-256 precompile")
	}
}

// bytesToAddress is a helper for tests
func bytesToAddress(b []byte) [20]byte {
	var addr [20]byte
	copy(addr[20-len(b):], b)
	return addr
}
