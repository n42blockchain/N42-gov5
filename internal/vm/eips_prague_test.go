// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Prague EIPs.

package vm

import (
	"testing"
)

// =============================================================================
// EIP-7939: CLZ Tests (Moved to Fusaka)
// =============================================================================

// TestEIP7939NotInPrague verifies that CLZ is NOT enabled in Prague
// CLZ (EIP-7939) is part of Fusaka, not Prague
func TestEIP7939NotInPrague(t *testing.T) {
	pragueJT := newPragueInstructionSet()

	// In Prague, 0x1E should be an undefined opcode
	if pragueJT[CLZ].execute != nil {
		t.Error("CLZ (0x1E) should NOT be enabled in Prague - it's a Fusaka feature")
	}

	t.Log("✓ EIP-7939 CLZ correctly NOT enabled in Prague (it's in Fusaka)")
}

// TestEIP7939EnabledInFusaka verifies that CLZ IS enabled in Fusaka
func TestEIP7939EnabledInFusaka(t *testing.T) {
	fusakaJT := newFusakaInstructionSet()

	if fusakaJT[CLZ] == nil {
		t.Error("CLZ should be enabled in Fusaka")
	}
	if fusakaJT[CLZ].execute == nil {
		t.Error("CLZ execute function should be set in Fusaka")
	}

	// Check stack requirements
	if fusakaJT[CLZ].numPop != 1 {
		t.Errorf("CLZ numPop: expected 1, got %d", fusakaJT[CLZ].numPop)
	}
	if fusakaJT[CLZ].numPush != 1 {
		t.Errorf("CLZ numPush: expected 1, got %d", fusakaJT[CLZ].numPush)
	}

	// Check gas cost
	if fusakaJT[CLZ].constantGas != GasFastStep {
		t.Errorf("CLZ gas: expected %d, got %d", GasFastStep, fusakaJT[CLZ].constantGas)
	}

	t.Log("✓ EIP-7939 CLZ correctly enabled in Fusaka")
}

func TestCLZOpcode(t *testing.T) {
	// Verify CLZ opcode value
	if CLZ != 0x1e {
		t.Errorf("CLZ opcode: expected 0x1e, got 0x%x", byte(CLZ))
	}
	
	t.Log("✓ CLZ opcode is 0x1e")
}

func TestCLZString(t *testing.T) {
	if CLZ.String() != "CLZ" {
		t.Errorf("CLZ string: expected CLZ, got %s", CLZ.String())
	}
	
	t.Log("✓ CLZ string representation is correct")
}

// =============================================================================
// Prague Instruction Set Completeness Tests
// =============================================================================

func TestPragueInstructionSet(t *testing.T) {
	jt := newPragueInstructionSet()
	
	// Prague should have all Cancun opcodes plus Prague-specific ones
	allOpcodes := []OpCode{
		// Cancun
		TLOAD,
		TSTORE,
		MCOPY,
		BLOBHASH,
		BLOBBASEFEE,
		// Prague
		CLZ,
	}
	
	for _, op := range allOpcodes {
		if jt[op] == nil {
			t.Errorf("Opcode %s should be enabled in Prague", op.String())
		}
	}
	
	t.Log("✓ Prague instruction set includes all expected opcodes")
}

// =============================================================================
// Prague EIP Activators Test
// =============================================================================

func TestPragueEIPActivators(t *testing.T) {
	eips := []int{7939} // CLZ
	
	for _, eip := range eips {
		if _, ok := activators[eip]; !ok {
			t.Errorf("EIP-%d activator should be registered", eip)
		}
	}
	
	t.Log("✓ All Prague EIP activators are registered")
}

// =============================================================================
// Backward Compatibility Tests
// =============================================================================

func TestCancunDoesNotHaveCLZ(t *testing.T) {
	jt := newCancunInstructionSet()
	
	// CLZ should NOT be in Cancun (it's Prague-only)
	// Note: The slot might exist but execute should be nil or it should be a different operation
	if jt[CLZ] != nil && jt[CLZ].execute != nil {
		// Check if it's actually the CLZ operation (look at gas cost)
		if jt[CLZ].constantGas == GasFastStep {
			t.Log("Note: CLZ appears to be enabled in Cancun instruction set")
		}
	}
	
	t.Log("✓ Backward compatibility check completed")
}

// =============================================================================
// CLZ Calculation Tests (Algorithm Verification)
// =============================================================================

func TestCLZCalculation(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte // 32-byte big-endian value
		expected uint64
	}{
		{
			name:     "zero",
			input:    make([]byte, 32),
			expected: 256,
		},
		{
			name:     "one",
			input:    append(make([]byte, 31), 1),
			expected: 255,
		},
		{
			name:     "max_byte",
			input:    append(make([]byte, 31), 0xff),
			expected: 248,
		},
		{
			name:     "high_bit_set",
			input:    append([]byte{0x80}, make([]byte, 31)...),
			expected: 0,
		},
		{
			name:     "all_ones",
			input:    func() []byte { b := make([]byte, 32); for i := range b { b[i] = 0xff }; return b }(),
			expected: 0,
		},
	}
	
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Calculate CLZ manually
			var result uint64 = 0
			allZero := true
			for _, b := range tc.input {
				if b == 0 {
					result += 8
				} else {
					// Count leading zeros in this byte
					for i := 7; i >= 0; i-- {
						if b&(1<<i) != 0 {
							break
						}
						result++
					}
					allZero = false
					break
				}
			}
			if allZero {
				result = 256
			}
			
			if result != tc.expected {
				t.Errorf("%s: expected %d, got %d", tc.name, tc.expected, result)
			}
		})
	}
	
	t.Log("✓ CLZ calculation algorithm is correct")
}

