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

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// EntryPoint Address Tests
// =============================================================================

func TestEntryPointAddresses(t *testing.T) {
	// Verify EntryPoint addresses are not zero
	if EntryPointV06 == (types.Address{}) {
		t.Error("EntryPointV06 should not be zero")
	}
	if EntryPointV07 == (types.Address{}) {
		t.Error("EntryPointV07 should not be zero")
	}
	if SenderCreator == (types.Address{}) {
		t.Error("SenderCreator should not be zero")
	}

	// Verify they are different
	if EntryPointV06 == EntryPointV07 {
		t.Error("EntryPointV06 and EntryPointV07 should be different")
	}
}

func TestIsEntryPoint(t *testing.T) {
	tests := []struct {
		addr   types.Address
		expect bool
	}{
		{EntryPointV06, true},
		{EntryPointV07, true},
		{SenderCreator, false},
		{types.Address{}, false},
	}

	for _, tt := range tests {
		got := IsEntryPoint(tt.addr)
		if got != tt.expect {
			t.Errorf("IsEntryPoint(%v) = %v, want %v", tt.addr, got, tt.expect)
		}
	}
}

func TestIsSenderCreator(t *testing.T) {
	if !IsSenderCreator(SenderCreator) {
		t.Error("IsSenderCreator(SenderCreator) should be true")
	}
	if IsSenderCreator(EntryPointV06) {
		t.Error("IsSenderCreator(EntryPointV06) should be false")
	}
}

// =============================================================================
// Gas Constants Tests
// =============================================================================

func TestERC4337GasConstants(t *testing.T) {
	if UserOperationCallGasLimit != 35000 {
		t.Errorf("UserOperationCallGasLimit = %d, want 35000", UserOperationCallGasLimit)
	}
	if VerificationGasLimit != 70000 {
		t.Errorf("VerificationGasLimit = %d, want 70000", VerificationGasLimit)
	}
	if PreVerificationGas != 21000 {
		t.Errorf("PreVerificationGas = %d, want 21000", PreVerificationGas)
	}
}

// =============================================================================
// UserOperation Tests
// =============================================================================

func TestUserOperationGetFactory(t *testing.T) {
	factory := types.HexToAddress("0x1234567890123456789012345678901234567890")
	initCode := append(factory.Bytes(), []byte{0x01, 0x02, 0x03}...)

	op := &UserOperation{
		InitCode: initCode,
	}

	got := op.GetFactory()
	if got != factory {
		t.Errorf("GetFactory() = %v, want %v", got, factory)
	}
}

func TestUserOperationGetFactoryEmpty(t *testing.T) {
	op := &UserOperation{
		InitCode: []byte{},
	}

	got := op.GetFactory()
	if got != (types.Address{}) {
		t.Errorf("GetFactory() for empty initCode = %v, want zero address", got)
	}
}

func TestUserOperationGetFactoryData(t *testing.T) {
	factory := types.HexToAddress("0x1234567890123456789012345678901234567890")
	factoryData := []byte{0x01, 0x02, 0x03, 0x04}
	initCode := append(factory.Bytes(), factoryData...)

	op := &UserOperation{
		InitCode: initCode,
	}

	got := op.GetFactoryData()
	if len(got) != len(factoryData) {
		t.Errorf("GetFactoryData() len = %d, want %d", len(got), len(factoryData))
	}
}

func TestUserOperationGetPaymaster(t *testing.T) {
	paymaster := types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01")
	paymasterData := []byte{0x01, 0x02, 0x03}
	paymasterAndData := append(paymaster.Bytes(), paymasterData...)

	op := &UserOperation{
		PaymasterAndData: paymasterAndData,
	}

	got := op.GetPaymaster()
	if got != paymaster {
		t.Errorf("GetPaymaster() = %v, want %v", got, paymaster)
	}
}

func TestUserOperationHasInitCode(t *testing.T) {
	op1 := &UserOperation{InitCode: []byte{0x01}}
	if !op1.HasInitCode() {
		t.Error("HasInitCode() should be true for non-empty initCode")
	}

	op2 := &UserOperation{InitCode: []byte{}}
	if op2.HasInitCode() {
		t.Error("HasInitCode() should be false for empty initCode")
	}
}

func TestUserOperationHasPaymaster(t *testing.T) {
	paymaster := types.HexToAddress("0x1234567890123456789012345678901234567890")
	op1 := &UserOperation{PaymasterAndData: paymaster.Bytes()}
	if !op1.HasPaymaster() {
		t.Error("HasPaymaster() should be true for 20-byte paymasterAndData")
	}

	op2 := &UserOperation{PaymasterAndData: []byte{}}
	if op2.HasPaymaster() {
		t.Error("HasPaymaster() should be false for empty paymasterAndData")
	}
}

// =============================================================================
// Validation Data Pack/Unpack Tests
// =============================================================================

func TestPackUnpackValidationData(t *testing.T) {
	original := &AccountValidationResult{
		ValidAfter: 1000,
		ValidUntil: 2000,
		Authorizer: types.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	packed := PackValidationData(original)
	if packed == nil {
		t.Fatal("PackValidationData returned nil")
	}

	unpacked := UnpackValidationData(packed)
	if unpacked.ValidAfter != original.ValidAfter {
		t.Errorf("ValidAfter = %d, want %d", unpacked.ValidAfter, original.ValidAfter)
	}
	if unpacked.ValidUntil != original.ValidUntil {
		t.Errorf("ValidUntil = %d, want %d", unpacked.ValidUntil, original.ValidUntil)
	}
}

func TestSigValidationConstants(t *testing.T) {
	if SIG_VALIDATION_SUCCEEDED != 0 {
		t.Errorf("SIG_VALIDATION_SUCCEEDED = %d, want 0", SIG_VALIDATION_SUCCEEDED)
	}
	if SIG_VALIDATION_FAILED != 1 {
		t.Errorf("SIG_VALIDATION_FAILED = %d, want 1", SIG_VALIDATION_FAILED)
	}
}

// =============================================================================
// Gas Calculation Tests
// =============================================================================

func TestCalcPreVerificationGas(t *testing.T) {
	op := &UserOperation{
		CallData:         make([]byte, 100), // 100 zero bytes = 400 gas
		InitCode:         []byte{},
		PaymasterAndData: []byte{},
		Signature:        make([]byte, 65),
	}

	gas := CalcPreVerificationGas(op, uint256.NewInt(0))

	// Should be at least PreVerificationGas + calldata gas
	if gas < PreVerificationGas {
		t.Errorf("CalcPreVerificationGas() = %d, should be >= %d", gas, PreVerificationGas)
	}
}

func TestCalcRequiredPrefund(t *testing.T) {
	op := &UserOperation{
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(200000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000), // 1 Gwei
	}

	prefund := CalcRequiredPrefund(op)

	// Expected: (100000 + 200000 + 50000) * 1e9 = 350000e9
	expected := new(uint256.Int).Mul(uint256.NewInt(350000), uint256.NewInt(1000000000))
	if prefund.Cmp(expected) != 0 {
		t.Errorf("CalcRequiredPrefund() = %v, want %v", prefund, expected)
	}
}

// =============================================================================
// Event Signatures Tests
// =============================================================================

func TestEventSignatures(t *testing.T) {
	// Verify event signatures are not zero
	if UserOperationEventSig == (types.Hash{}) {
		t.Error("UserOperationEventSig should not be zero")
	}
	if AccountDeployedSig == (types.Hash{}) {
		t.Error("AccountDeployedSig should not be zero")
	}
	if DepositedSig == (types.Hash{}) {
		t.Error("DepositedSig should not be zero")
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCalcPreVerificationGas(b *testing.B) {
	op := &UserOperation{
		CallData:         make([]byte, 1000),
		InitCode:         make([]byte, 100),
		PaymasterAndData: make([]byte, 100),
		Signature:        make([]byte, 65),
	}
	baseFee := uint256.NewInt(1000000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcPreVerificationGas(op, baseFee)
	}
}

func BenchmarkCalcRequiredPrefund(b *testing.B) {
	op := &UserOperation{
		CallGasLimit:         uint256.NewInt(100000),
		VerificationGasLimit: uint256.NewInt(200000),
		PreVerificationGas:   uint256.NewInt(50000),
		MaxFeePerGas:         uint256.NewInt(1000000000),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcRequiredPrefund(op)
	}
}

func BenchmarkPackValidationData(b *testing.B) {
	result := &AccountValidationResult{
		ValidAfter: 1000,
		ValidUntil: 2000,
		Authorizer: types.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PackValidationData(result)
	}
}

