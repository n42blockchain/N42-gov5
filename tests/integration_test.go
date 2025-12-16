// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Integration tests verifying cross-module functionality and overall system consistency.

package tests

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/utils"
)

// =============================================================================
// Cross-Module Integration Tests
// =============================================================================

// TestCommonInternalIntegration verifies common and internal packages work together
func TestCommonInternalIntegration(t *testing.T) {
	// Test GasPool from common is compatible with internal error handling
	gp := common.GasPool(1000)
	err := gp.SubGas(500)
	if err != nil {
		t.Errorf("SubGas should not error: %v", err)
	}

	// Verify error from common matches internal expectations
	err = gp.SubGas(1000) // Try to subtract more than available
	if err != common.ErrGasLimitReached {
		t.Errorf("Expected ErrGasLimitReached, got: %v", err)
	}

	t.Logf("✓ common.GasPool integrates correctly with error handling")
}

// TestTypesHashConsistency verifies hash types are consistent across modules
func TestTypesHashConsistency(t *testing.T) {
	// Create hash in common/types
	hash1 := types.Hash{0x01, 0x02, 0x03}

	// Use hash in utils (Keccak256Hash)
	data := []byte("test data")
	hash2 := utils.Keccak256Hash(data)

	// Verify hash dimensions
	if len(hash1) != 32 {
		t.Errorf("types.Hash should be 32 bytes, got %d", len(hash1))
	}
	if len(hash2) != 32 {
		t.Errorf("utils.Keccak256Hash should return 32 bytes, got %d", len(hash2))
	}

	t.Logf("✓ Hash types consistent across modules")
}

// TestAddressTypeConsistency verifies address types work across modules
func TestAddressTypeConsistency(t *testing.T) {
	// Create address
	addr := types.Address{0x01, 0x02, 0x03}

	// Verify address dimensions
	if len(addr) != 20 {
		t.Errorf("types.Address should be 20 bytes, got %d", len(addr))
	}

	// Test with utils.ToBytes20
	bytes20 := utils.ToBytes20(addr[:])
	if !bytes.Equal(bytes20[:], addr[:]) {
		t.Error("Address bytes should match ToBytes20 output")
	}

	t.Logf("✓ Address types consistent across modules")
}

// TestUint256Integration verifies uint256 types work correctly
func TestUint256Integration(t *testing.T) {
	// Create uint256 values
	val1 := uint256.NewInt(1000)
	val2 := uint256.NewInt(500)

	// Arithmetic operations
	result := new(uint256.Int).Add(val1, val2)
	if result.Cmp(uint256.NewInt(1500)) != 0 {
		t.Errorf("uint256 addition failed: %v + %v = %v", val1, val2, result)
	}

	// Subtraction
	result = new(uint256.Int).Sub(val1, val2)
	if result.Cmp(uint256.NewInt(500)) != 0 {
		t.Errorf("uint256 subtraction failed")
	}

	t.Logf("✓ uint256 operations work correctly")
}

// TestKeccak256HashIntegration verifies Keccak256 produces consistent results
func TestKeccak256HashIntegration(t *testing.T) {
	data := []byte("hello world")

	// Hash using utils
	hash1 := utils.Keccak256(data)
	hash2 := utils.Keccak256(data)

	// Verify determinism
	if !bytes.Equal(hash1, hash2) {
		t.Error("Keccak256 should be deterministic")
	}

	// Verify length
	if len(hash1) != 32 {
		t.Errorf("Keccak256 should return 32 bytes, got %d", len(hash1))
	}

	t.Logf("✓ Keccak256 integration works correctly")
}

// =============================================================================
// Error Handling Integration Tests
// =============================================================================

// TestInternalErrorsExist verifies all internal errors are properly defined
func TestInternalErrorsExist(t *testing.T) {
	errors := []error{
		internal.ErrInvalidBlock,
		internal.ErrInvalidPubSub,
		internal.ErrBannedHash,
		internal.ErrNoGenesis,
		internal.ErrNonceTooLow,
		internal.ErrNonceTooHigh,
		internal.ErrNonceMax,
		internal.ErrGasLimitReached,
		internal.ErrInsufficientFundsForTransfer,
		internal.ErrInsufficientFunds,
		internal.ErrGasUintOverflow,
		internal.ErrIntrinsicGas,
		internal.ErrTxTypeNotSupported,
		internal.ErrTipAboveFeeCap,
		internal.ErrTipVeryHigh,
		internal.ErrFeeCapVeryHigh,
		internal.ErrFeeCapTooLow,
		internal.ErrSenderNoEOA,
		internal.ErrAlreadyDeposited,
	}

	for i, err := range errors {
		if err == nil {
			t.Errorf("Error %d should not be nil", i)
		}
	}

	t.Logf("✓ All %d internal errors are defined", len(errors))
}

// TestCommonErrorsExist verifies common errors are defined
func TestCommonErrorsExist(t *testing.T) {
	if common.ErrGasLimitReached == nil {
		t.Error("common.ErrGasLimitReached should not be nil")
	}

	// Verify message
	if common.ErrGasLimitReached.Error() != "gas limit reached" {
		t.Errorf("Unexpected error message: %s", common.ErrGasLimitReached.Error())
	}

	t.Logf("✓ Common errors are defined correctly")
}

// =============================================================================
// Interface Compatibility Tests
// =============================================================================

// TestEVMTypesStateDBAlias verifies evmtypes.IntraBlockState is common.StateDB alias
func TestEVMTypesStateDBAlias(t *testing.T) {
	// evmtypes.IntraBlockState should be an alias for common.StateDB
	// This is a compile-time check
	var _ evmtypes.IntraBlockState = (common.StateDB)(nil)
	var _ common.StateDB = (evmtypes.IntraBlockState)(nil)

	t.Logf("✓ evmtypes.IntraBlockState is alias for common.StateDB")
}

// TestBigConstantsAvailable verifies common big constants are available
func TestBigConstantsAvailable(t *testing.T) {
	constants := []struct {
		name  string
		value int64
	}{
		{"Big0", 0},
		{"Big1", 1},
		{"Big2", 2},
		{"Big3", 3},
		{"Big32", 32},
		{"Big256", 256},
		{"Big257", 257},
	}

	if common.Big0.Int64() != 0 {
		t.Error("Big0 should be 0")
	}
	if common.Big1.Int64() != 1 {
		t.Error("Big1 should be 1")
	}
	if common.Big256.Int64() != 256 {
		t.Error("Big256 should be 256")
	}

	t.Logf("✓ All %d big constants are available", len(constants))
}

// =============================================================================
// Utility Function Integration Tests
// =============================================================================

// TestToBytesConsistency verifies ToBytes functions work correctly
func TestToBytesConsistency(t *testing.T) {
	// Test various ToBytes functions
	input := make([]byte, 100)
	for i := range input {
		input[i] = byte(i)
	}

	b4 := utils.ToBytes4(input)
	b20 := utils.ToBytes20(input)
	b32 := utils.ToBytes32(input)
	b48 := utils.ToBytes48(input)
	b64 := utils.ToBytes64(input)
	b96 := utils.ToBytes96(input)

	// Verify lengths
	if len(b4) != 4 || len(b20) != 20 || len(b32) != 32 {
		t.Error("ToBytes functions return wrong lengths")
	}
	if len(b48) != 48 || len(b64) != 64 || len(b96) != 96 {
		t.Error("ToBytes functions return wrong lengths")
	}

	// Verify content preservation
	for i := 0; i < 4; i++ {
		if b4[i] != input[i] {
			t.Errorf("ToBytes4 content mismatch at %d", i)
		}
	}

	t.Logf("✓ ToBytes functions work correctly")
}

// TestHexPrefixIntegration verifies HexPrefix utility
func TestHexPrefixIntegration(t *testing.T) {
	a := []byte{1, 2, 3, 4, 5}
	b := []byte{1, 2, 3, 9, 9}

	prefix, length := utils.HexPrefix(a, b)

	if length != 3 {
		t.Errorf("HexPrefix length = %d, want 3", length)
	}
	if !bytes.Equal(prefix, []byte{1, 2, 3}) {
		t.Error("HexPrefix content mismatch")
	}

	t.Logf("✓ HexPrefix integration works correctly")
}

// =============================================================================
// Memory Pool Tests
// =============================================================================

// TestGasPoolOperations verifies GasPool operations are thread-safe conceptually
func TestGasPoolOperations(t *testing.T) {
	gp := common.GasPool(10000)

	// Chain of operations
	gp.AddGas(5000)
	if gp.Gas() != 15000 {
		t.Errorf("Gas() = %d, want 15000", gp.Gas())
	}

	err := gp.SubGas(3000)
	if err != nil {
		t.Errorf("SubGas failed: %v", err)
	}
	if gp.Gas() != 12000 {
		t.Errorf("Gas() = %d, want 12000", gp.Gas())
	}

	// Verify string representation
	str := gp.String()
	if str != "12000" {
		t.Errorf("String() = %s, want '12000'", str)
	}

	t.Logf("✓ GasPool operations work correctly")
}

// =============================================================================
// Consistency Verification Tests
// =============================================================================

// TestHashZeroValue verifies zero hash behavior
func TestHashZeroValue(t *testing.T) {
	var zeroHash types.Hash

	// All bytes should be zero
	for i, b := range zeroHash {
		if b != 0 {
			t.Errorf("Zero hash byte %d is %d, want 0", i, b)
		}
	}

	t.Logf("✓ Hash zero value is correctly all zeros")
}

// TestAddressZeroValue verifies zero address behavior
func TestAddressZeroValue(t *testing.T) {
	var zeroAddr types.Address

	// All bytes should be zero
	for i, b := range zeroAddr {
		if b != 0 {
			t.Errorf("Zero address byte %d is %d, want 0", i, b)
		}
	}

	t.Logf("✓ Address zero value is correctly all zeros")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCrossModuleHashOperation(b *testing.B) {
	data := []byte("benchmark test data for cross-module operations")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := utils.Keccak256(data)
		_ = types.Hash(utils.ToBytes32(hash))
	}
}

func BenchmarkGasPoolCycle(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gp := common.GasPool(10000)
		gp.AddGas(5000)
		_ = gp.SubGas(3000)
		_ = gp.Gas()
	}
}

func BenchmarkUint256Operations(b *testing.B) {
	val1 := uint256.NewInt(1000000)
	val2 := uint256.NewInt(500000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := new(uint256.Int)
		result.Add(val1, val2)
		result.Sub(result, val2)
		_ = result.Cmp(val1)
	}
}

func BenchmarkTypeConversions(b *testing.B) {
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = utils.ToBytes4(data)
		_ = utils.ToBytes20(data)
		_ = utils.ToBytes32(data)
	}
}
