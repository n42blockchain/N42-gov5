// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Cancun EIPs.

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// EIP-1153: Transient Storage Tests
// =============================================================================

func TestEIP1153Enabled(t *testing.T) {
	jt := newCancunInstructionSet()
	
	// Check TLOAD is enabled
	if jt[TLOAD] == nil {
		t.Error("TLOAD should be enabled in Cancun")
	}
	if jt[TLOAD].execute == nil {
		t.Error("TLOAD execute function should be set")
	}
	
	// Check TSTORE is enabled
	if jt[TSTORE] == nil {
		t.Error("TSTORE should be enabled in Cancun")
	}
	if jt[TSTORE].execute == nil {
		t.Error("TSTORE execute function should be set")
	}
	
	t.Log("✓ EIP-1153 TLOAD/TSTORE enabled in Cancun")
}

func TestEIP1153GasCost(t *testing.T) {
	jt := newCancunInstructionSet()
	
	// TLOAD and TSTORE should use warm storage read cost
	expectedGas := params.WarmStorageReadCostEIP2929
	
	if jt[TLOAD].constantGas != expectedGas {
		t.Errorf("TLOAD gas: expected %d, got %d", expectedGas, jt[TLOAD].constantGas)
	}
	
	if jt[TSTORE].constantGas != expectedGas {
		t.Errorf("TSTORE gas: expected %d, got %d", expectedGas, jt[TSTORE].constantGas)
	}
	
	t.Log("✓ EIP-1153 gas costs are correct")
}

// =============================================================================
// EIP-5656: MCOPY Tests
// =============================================================================

func TestEIP5656Enabled(t *testing.T) {
	jt := newCancunInstructionSet()
	
	if jt[MCOPY] == nil {
		t.Error("MCOPY should be enabled in Cancun")
	}
	if jt[MCOPY].execute == nil {
		t.Error("MCOPY execute function should be set")
	}
	
	// Check stack requirements
	if jt[MCOPY].numPop != 3 {
		t.Errorf("MCOPY numPop: expected 3, got %d", jt[MCOPY].numPop)
	}
	if jt[MCOPY].numPush != 0 {
		t.Errorf("MCOPY numPush: expected 0, got %d", jt[MCOPY].numPush)
	}
	
	t.Log("✓ EIP-5656 MCOPY enabled in Cancun")
}

func TestMemoryCopy(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)
	
	// Write test data
	testData := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	mem.Set(0, 8, testData)
	
	// Copy within memory
	mem.Copy(32, 0, 8)
	
	// Verify copy
	copied := mem.GetCopy(32, 8)
	for i, b := range testData {
		if copied[i] != b {
			t.Errorf("Copy mismatch at %d: expected %d, got %d", i, b, copied[i])
		}
	}
	
	t.Log("✓ Memory.Copy works correctly")
}

func TestMemoryCopyOverlap(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)
	
	// Write test data
	for i := 0; i < 16; i++ {
		mem.store[i] = byte(i + 1)
	}
	
	// Overlapping copy (src: 0, dst: 4, len: 8)
	mem.Copy(4, 0, 8)
	
	// Expected: [1,2,3,4,1,2,3,4,5,6,7,8,13,14,15,16...]
	expected := []byte{1, 2, 3, 4, 1, 2, 3, 4, 5, 6, 7, 8}
	for i, b := range expected {
		if mem.store[i] != b {
			t.Errorf("Overlap copy mismatch at %d: expected %d, got %d", i, b, mem.store[i])
		}
	}
	
	t.Log("✓ Memory.Copy handles overlapping regions correctly")
}

// =============================================================================
// EIP-4844: BLOBHASH Tests
// =============================================================================

func TestEIP4844Enabled(t *testing.T) {
	jt := newCancunInstructionSet()
	
	if jt[BLOBHASH] == nil {
		t.Error("BLOBHASH should be enabled in Cancun")
	}
	if jt[BLOBHASH].execute == nil {
		t.Error("BLOBHASH execute function should be set")
	}
	
	t.Log("✓ EIP-4844 BLOBHASH enabled in Cancun")
}

// =============================================================================
// EIP-7516: BLOBBASEFEE Tests
// =============================================================================

func TestEIP7516Enabled(t *testing.T) {
	jt := newCancunInstructionSet()
	
	if jt[BLOBBASEFEE] == nil {
		t.Error("BLOBBASEFEE should be enabled in Cancun")
	}
	if jt[BLOBBASEFEE].execute == nil {
		t.Error("BLOBBASEFEE execute function should be set")
	}
	
	// Check gas cost
	if jt[BLOBBASEFEE].constantGas != GasQuickStep {
		t.Errorf("BLOBBASEFEE gas: expected %d, got %d", GasQuickStep, jt[BLOBBASEFEE].constantGas)
	}
	
	t.Log("✓ EIP-7516 BLOBBASEFEE enabled in Cancun")
}

// =============================================================================
// Cancun Instruction Set Completeness Tests
// =============================================================================

func TestCancunInstructionSet(t *testing.T) {
	jt := newCancunInstructionSet()
	
	// All Cancun-specific opcodes should be enabled
	cancunOpcodes := []OpCode{
		TLOAD,       // EIP-1153
		TSTORE,      // EIP-1153
		MCOPY,       // EIP-5656
		BLOBHASH,    // EIP-4844
		BLOBBASEFEE, // EIP-7516
	}
	
	for _, op := range cancunOpcodes {
		if jt[op] == nil {
			t.Errorf("Opcode %s should be enabled in Cancun", op.String())
		}
	}
	
	t.Log("✓ All Cancun opcodes are enabled")
}

// =============================================================================
// Gas Calculation Tests
// =============================================================================

func TestSafeMul(t *testing.T) {
	tests := []struct {
		a, b     uint64
		expected uint64
		overflow bool
	}{
		{0, 0, 0, false},
		{1, 1, 1, false},
		{100, 200, 20000, false},
		{1 << 32, 1 << 32, 0, true}, // overflow
	}
	
	for _, tc := range tests {
		result, overflow := safeMul(tc.a, tc.b)
		if overflow != tc.overflow {
			t.Errorf("safeMul(%d, %d): overflow expected %v, got %v", tc.a, tc.b, tc.overflow, overflow)
		}
		if !overflow && result != tc.expected {
			t.Errorf("safeMul(%d, %d): expected %d, got %d", tc.a, tc.b, tc.expected, result)
		}
	}
	
	t.Log("✓ safeMul works correctly")
}

func TestSafeAdd(t *testing.T) {
	tests := []struct {
		a, b     uint64
		expected uint64
		overflow bool
	}{
		{0, 0, 0, false},
		{1, 1, 2, false},
		{^uint64(0), 1, 0, true}, // overflow
		{^uint64(0) - 1, 1, ^uint64(0), false},
	}
	
	for _, tc := range tests {
		result, overflow := safeAdd(tc.a, tc.b)
		if overflow != tc.overflow {
			t.Errorf("safeAdd(%d, %d): overflow expected %v, got %v", tc.a, tc.b, tc.overflow, overflow)
		}
		if !overflow && result != tc.expected {
			t.Errorf("safeAdd(%d, %d): expected %d, got %d", tc.a, tc.b, tc.expected, result)
		}
	}
	
	t.Log("✓ safeAdd works correctly")
}

func TestToWordSize(t *testing.T) {
	tests := []struct {
		size     uint64
		expected uint64
	}{
		{0, 0},
		{1, 1},
		{32, 1},
		{33, 2},
		{64, 2},
		{65, 3},
	}
	
	for _, tc := range tests {
		result := toWordSize(tc.size)
		if result != tc.expected {
			t.Errorf("toWordSize(%d): expected %d, got %d", tc.size, tc.expected, result)
		}
	}
	
	t.Log("✓ toWordSize works correctly")
}

// =============================================================================
// Backward Compatibility Tests
// =============================================================================

func TestCancunHasAllShanghaiOpcodes(t *testing.T) {
	shanghaiJt := newShanghaiInstructionSet()
	cancunJt := newCancunInstructionSet()
	
	// Cancun should have all Shanghai opcodes
	shanghaiOpcodes := []OpCode{
		PUSH0, // EIP-3855
	}
	
	for _, op := range shanghaiOpcodes {
		if shanghaiJt[op] == nil || shanghaiJt[op].execute == nil {
			t.Errorf("Opcode %s should be in Shanghai", op.String())
		}
		if cancunJt[op] == nil || cancunJt[op].execute == nil {
			t.Errorf("Opcode %s should also be in Cancun", op.String())
		}
	}
	
	t.Log("✓ Cancun includes all Shanghai opcodes")
}

// =============================================================================
// EIP Activators Test
// =============================================================================

func TestCancunEIPActivators(t *testing.T) {
	eips := []int{1153, 5656, 4844, 7516, 6780}
	
	for _, eip := range eips {
		if _, ok := activators[eip]; !ok {
			t.Errorf("EIP-%d activator should be registered", eip)
		}
	}
	
	t.Log("✓ All Cancun EIP activators are registered")
}

// =============================================================================
// Opcode String Tests
// =============================================================================

func TestCancunOpcodeStrings(t *testing.T) {
	opcodes := map[OpCode]string{
		TLOAD:       "TLOAD",
		TSTORE:      "TSTORE",
		MCOPY:       "MCOPY",
		BLOBHASH:    "BLOBHASH",
		BLOBBASEFEE: "BLOBBASEFEE",
	}
	
	for op, expected := range opcodes {
		if op.String() != expected {
			t.Errorf("Opcode 0x%x: expected %s, got %s", byte(op), expected, op.String())
		}
	}
	
	t.Log("✓ Cancun opcode strings are correct")
}

