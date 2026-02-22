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
	"encoding/binary"
	"math"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// buildModExpInput constructs a MODEXP precompile input with the given
// base/exp/mod lengths encoded as big-endian uint64 in 32-byte slots,
// and fills the payload region with fillByte.
func buildModExpInput(baseLen, expLen, modLen uint64, fillByte byte) []byte {
	input := make([]byte, 96+baseLen+expLen+modLen)
	binary.BigEndian.PutUint64(input[24:32], baseLen)
	binary.BigEndian.PutUint64(input[56:64], expLen)
	binary.BigEndian.PutUint64(input[88:96], modLen)
	for i := uint64(96); i < uint64(len(input)); i++ {
		input[i] = fillByte
	}
	return input
}

func TestModExpEIP7823InputLimits(t *testing.T) {
	tests := []struct {
		name       string
		baseLen    uint64
		expLen     uint64
		modLen     uint64
		eip7823    bool
		wantMaxGas bool
		wantErr    bool
	}{
		{
			name:    "within limit - base 1024",
			baseLen: 1024, expLen: 32, modLen: 32,
			eip7823: true,
		},
		{
			name:       "exceeds limit - base 1025",
			baseLen:    1025, expLen: 32, modLen: 32,
			eip7823:    true,
			wantMaxGas: true, wantErr: true,
		},
		{
			name:       "exceeds limit - exp 1025",
			baseLen:    32, expLen: 1025, modLen: 32,
			eip7823:    true,
			wantMaxGas: true, wantErr: true,
		},
		{
			name:       "exceeds limit - mod 1025",
			baseLen:    32, expLen: 32, modLen: 1025,
			eip7823:    true,
			wantMaxGas: true, wantErr: true,
		},
		{
			name:    "no limit without eip7823 - base 2048",
			baseLen: 2048, expLen: 32, modLen: 32,
			eip7823: false,
		},
		{
			name:    "all at limit",
			baseLen: 1024, expLen: 1024, modLen: 1024,
			eip7823: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := buildModExpInput(tt.baseLen, tt.expLen, tt.modLen, 1)
			c := &bigModExp{eip2565: true, eip7823: tt.eip7823, eip7883: false}

			gas := c.RequiredGas(input)
			if tt.wantMaxGas && gas != math.MaxUint64 {
				t.Errorf("RequiredGas() = %d, want math.MaxUint64", gas)
			}

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

func TestModExpEIP7883GasCalculation(t *testing.T) {
	tests := []struct {
		name      string
		baseLen   uint64
		expLen    uint64
		modLen    uint64
		expValue  byte
		eip7883   bool
		wantMin   uint64
		checkMult bool
	}{
		{
			name:     "minimum gas with eip7883 - small inputs",
			baseLen:  1, expLen: 1, modLen: 1,
			expValue: 1, eip7883: true,
			wantMin: 500,
		},
		{
			name:     "minimum gas without eip7883 - small inputs",
			baseLen:  1, expLen: 1, modLen: 1,
			expValue: 1, eip7883: false,
			wantMin: 200,
		},
		{
			name:      "3x multiplier with larger inputs",
			baseLen:   64, expLen: 64, modLen: 64,
			expValue:  0xff, eip7883: true,
			checkMult: true,
		},
		{
			name:      "3x multiplier with medium inputs",
			baseLen:   128, expLen: 128, modLen: 128,
			expValue:  0xff, eip7883: true,
			checkMult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			totalLen := 96 + tt.baseLen + tt.expLen + tt.modLen
			input := make([]byte, totalLen)
			input[31] = byte(tt.baseLen)
			input[63] = byte(tt.expLen)
			input[95] = byte(tt.modLen)

			if tt.baseLen > 0 {
				input[96+tt.baseLen-1] = 2
			}
			if tt.expLen > 0 {
				input[96+tt.baseLen+tt.expLen-1] = tt.expValue
			}
			if tt.modLen > 0 {
				input[96+tt.baseLen+tt.expLen+tt.modLen-1] = 3
			}

			if tt.checkMult {
				c7883 := &bigModExp{eip2565: true, eip7823: false, eip7883: true}
				cBase := &bigModExp{eip2565: true, eip7823: false, eip7883: false}

				gas7883 := c7883.RequiredGas(input)
				gasBase := cBase.RequiredGas(input)

				expectedGas := gasBase * 3
				if expectedGas < 500 {
					expectedGas = 500
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

func TestModExpFusakaPrecompile(t *testing.T) {
	fusaka := PrecompiledContractsFusaka[types.BytesToAddress([]byte{5})]
	if fusaka == nil {
		t.Fatal("Fusaka MODEXP precompile not found at address 0x05")
	}

	// Input with baseLen=1025 exceeding EIP-7823 limit
	input := make([]byte, 96+1025+32+32)
	input[31] = 0x01
	input[30] = 0x04 // 0x0401 = 1025
	input[63] = 32
	input[95] = 32

	gas := fusaka.RequiredGas(input)
	if gas != math.MaxUint64 {
		t.Errorf("Fusaka MODEXP RequiredGas() = %d for oversized input, want math.MaxUint64", gas)
	}

	if _, err := fusaka.Run(input); err == nil {
		t.Error("Fusaka MODEXP Run() should return error for oversized input")
	}

	// Valid input: 2^10 mod 13 = 10
	validInput := make([]byte, 96+32+32+32)
	validInput[31] = 32
	validInput[63] = 32
	validInput[95] = 32
	validInput[96+31] = 2
	validInput[96+32+31] = 10
	validInput[96+32+32+31] = 13

	validGas := fusaka.RequiredGas(validInput)
	if validGas < 500 {
		t.Errorf("Fusaka MODEXP RequiredGas() = %d, want >= 500 (EIP-7883 minimum)", validGas)
	}

	result, err := fusaka.Run(validInput)
	if err != nil {
		t.Fatalf("Fusaka MODEXP Run() error = %v", err)
	}
	if len(result) != 32 {
		t.Fatalf("Fusaka MODEXP Run() result length = %d, want 32", len(result))
	}
	if result[31] != 10 {
		t.Errorf("Fusaka MODEXP Run() result[31] = %d, want 10 (2^10 mod 13)", result[31])
	}
}

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
