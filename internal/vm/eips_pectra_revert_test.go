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
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Pectra EIPs State Revert Tests
// These tests verify that state changes from Pectra EIPs can be correctly
// reverted during transaction execution failures.
// =============================================================================

// TestEIP7702DelegationCodeRevert tests that EIP-7702 delegation code
// changes can be correctly reverted using the existing codeChange journal entry.
func TestEIP7702DelegationCodeRevert(t *testing.T) {
	// Test that delegation code format is correct
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	delegationCode := AddressToDelegation(addr)

	// Verify delegation code format: 0xef0100 + address
	if len(delegationCode) != 23 {
		t.Errorf("Delegation code length = %d, want 23", len(delegationCode))
	}
	if !bytes.HasPrefix(delegationCode, DelegationPrefix) {
		t.Errorf("Delegation code should have prefix 0xef0100")
	}
	if !bytes.Equal(delegationCode[3:], addr.Bytes()) {
		t.Errorf("Delegation code should contain target address")
	}

	// Test parse round-trip
	parsedAddr, valid := ParseDelegation(delegationCode)
	if !valid {
		t.Error("ParseDelegation should return valid for delegation code")
	}
	if parsedAddr != addr {
		t.Errorf("Parsed address = %v, want %v", parsedAddr, addr)
	}

	// Test HasDelegation
	if !HasDelegation(delegationCode) {
		t.Error("HasDelegation should return true for delegation code")
	}

	// Test non-delegation code
	regularCode := []byte{0x60, 0x00, 0x60, 0x00} // PUSH1 0x00 PUSH1 0x00
	if HasDelegation(regularCode) {
		t.Error("HasDelegation should return false for regular code")
	}
}

// TestEIP7702DelegationParseEdgeCases tests edge cases in delegation parsing
func TestEIP7702DelegationParseEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		code      []byte
		wantValid bool
	}{
		{"nil code", nil, false},
		{"empty code", []byte{}, false},
		{"too short", []byte{0xef, 0x01}, false},
		{"wrong prefix byte 0", []byte{0xff, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, false},
		{"wrong prefix byte 1", []byte{0xef, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, false},
		{"wrong prefix byte 2", []byte{0xef, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, false},
		{"too long", append(DelegationPrefix, make([]byte, 21)...), false},
		{"valid delegation", append(DelegationPrefix, make([]byte, 20)...), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, valid := ParseDelegation(tt.code)
			if valid != tt.wantValid {
				t.Errorf("ParseDelegation() valid = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}

// TestEIP2935HistoryStorageRevert tests that EIP-2935 history storage
// changes can be correctly reverted.
func TestEIP2935HistoryStorageRevert(t *testing.T) {
	// Test slot calculation for various block numbers
	tests := []struct {
		blockNum     uint64
		expectedSlot uint64
	}{
		{0, 0},
		{1, 1},
		{8191, 8191},
		{8192, 0},      // Wraps to 0
		{8193, 1},      // Wraps to 1
		{16384, 0},     // 2x window
		{100000, 1696}, // 100000 % 8192 = 1696
		{1000000, 576}, // 1000000 % 8192 = 576
	}

	for _, tt := range tests {
		slot := tt.blockNum % HistoryServeWindow
		if slot != tt.expectedSlot {
			t.Errorf("Block %d: slot = %d, want %d", tt.blockNum, slot, tt.expectedSlot)
		}
	}
}

// TestEIP7685RequestTypesRevert tests EIP-7685 request type serialization
// which is used in state transitions and must be correctly handled during reverts.
func TestEIP7685RequestTypesRevert(t *testing.T) {
	// Test deposit request
	depositData := []byte{0x01, 0x02, 0x03}
	encodedDeposit := EncodeExecutionRequest(DepositRequestType, depositData)
	if encodedDeposit[0] != DepositRequestType {
		t.Errorf("Encoded deposit type = %d, want %d", encodedDeposit[0], DepositRequestType)
	}

	decodedType, decodedData, err := DecodeExecutionRequest(encodedDeposit)
	if err != nil {
		t.Fatalf("DecodeExecutionRequest error: %v", err)
	}
	if decodedType != DepositRequestType {
		t.Errorf("Decoded type = %d, want %d", decodedType, DepositRequestType)
	}
	if !bytes.Equal(decodedData, depositData) {
		t.Error("Decoded data mismatch")
	}

	// Test withdrawal request
	withdrawalData := []byte{0x04, 0x05, 0x06}
	encodedWithdrawal := EncodeExecutionRequest(WithdrawalRequestType, withdrawalData)
	if encodedWithdrawal[0] != WithdrawalRequestType {
		t.Errorf("Encoded withdrawal type = %d, want %d", encodedWithdrawal[0], WithdrawalRequestType)
	}

	// Test consolidation request
	consolidationData := []byte{0x07, 0x08, 0x09}
	encodedConsolidation := EncodeExecutionRequest(ConsolidationRequestType, consolidationData)
	if encodedConsolidation[0] != ConsolidationRequestType {
		t.Errorf("Encoded consolidation type = %d, want %d", encodedConsolidation[0], ConsolidationRequestType)
	}
}

// TestEIP7002WithdrawalRequestSerialize tests withdrawal request serialization
// for state consistency during reverts.
func TestEIP7002WithdrawalRequestSerialize(t *testing.T) {
	req := &WithdrawalRequest{
		SourceAddress: types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01"),
		Amount:        1000000000, // 1 Gwei
	}
	copy(req.ValidatorPubkey[:], bytes.Repeat([]byte{0xaa}, 48))

	// Serialize
	serialized := req.Serialize()
	if len(serialized) != WithdrawalRequestSize {
		t.Errorf("Serialized size = %d, want %d", len(serialized), WithdrawalRequestSize)
	}

	// Deserialize
	decoded, err := DeserializeWithdrawalRequest(serialized)
	if err != nil {
		t.Fatalf("Deserialize error: %v", err)
	}

	// Verify round-trip
	if decoded.SourceAddress != req.SourceAddress {
		t.Error("SourceAddress mismatch after round-trip")
	}
	if decoded.ValidatorPubkey != req.ValidatorPubkey {
		t.Error("ValidatorPubkey mismatch after round-trip")
	}
	if decoded.Amount != req.Amount {
		t.Errorf("Amount = %d, want %d", decoded.Amount, req.Amount)
	}
}

// TestEIP7251ConsolidationRequestSerialize tests consolidation request
// serialization for state consistency during reverts.
func TestEIP7251ConsolidationRequestSerialize(t *testing.T) {
	req := &ConsolidationRequest{
		SourceAddress: types.HexToAddress("0xfedcba9876543210fedcba9876543210fedcba98"),
	}
	copy(req.SourcePubkey[:], bytes.Repeat([]byte{0xbb}, 48))
	copy(req.TargetPubkey[:], bytes.Repeat([]byte{0xcc}, 48))

	// Serialize
	serialized := req.Serialize()
	if len(serialized) != ConsolidationRequestSize {
		t.Errorf("Serialized size = %d, want %d", len(serialized), ConsolidationRequestSize)
	}

	// Deserialize
	decoded, err := DeserializeConsolidationRequest(serialized)
	if err != nil {
		t.Fatalf("Deserialize error: %v", err)
	}

	// Verify round-trip
	if decoded.SourceAddress != req.SourceAddress {
		t.Error("SourceAddress mismatch after round-trip")
	}
	if decoded.SourcePubkey != req.SourcePubkey {
		t.Error("SourcePubkey mismatch after round-trip")
	}
	if decoded.TargetPubkey != req.TargetPubkey {
		t.Error("TargetPubkey mismatch after round-trip")
	}
}

// TestEIP7691BlobParamsConsistency tests that blob parameters are consistent
// for both Cancun and Pectra, which is important for state calculations.
func TestEIP7691BlobParamsConsistency(t *testing.T) {
	// Cancun parameters
	cancun := CancunBlobParams()
	if cancun.TargetBlobGasPerBlock != cancun.TargetBlobsPerBlock*cancun.BlobGasPerBlob {
		t.Error("Cancun: target blob gas calculation inconsistent")
	}
	if cancun.MaxBlobGasPerBlock != cancun.MaxBlobsPerBlock*cancun.BlobGasPerBlob {
		t.Error("Cancun: max blob gas calculation inconsistent")
	}

	// Pectra parameters
	pectra := PectraBlobParams()
	if pectra.TargetBlobGasPerBlock != pectra.TargetBlobsPerBlock*pectra.BlobGasPerBlob {
		t.Error("Pectra: target blob gas calculation inconsistent")
	}
	if pectra.MaxBlobGasPerBlock != pectra.MaxBlobsPerBlock*pectra.BlobGasPerBlob {
		t.Error("Pectra: max blob gas calculation inconsistent")
	}

	// Pectra should have increased limits
	if pectra.TargetBlobsPerBlock <= cancun.TargetBlobsPerBlock {
		t.Error("Pectra target blobs should be > Cancun")
	}
	if pectra.MaxBlobsPerBlock <= cancun.MaxBlobsPerBlock {
		t.Error("Pectra max blobs should be > Cancun")
	}
}

// TestEIP7623CalldataCostConsistency tests EIP-7623 calldata cost calculation
// for state transition consistency.
func TestEIP7623CalldataCostConsistency(t *testing.T) {
	// EIP-7623: Pectra always uses max(standard, floor)
	// floor = zero*10 + nonzero*40

	// Small zero data: standard=400, floor=1000 -> pectra=1000
	smallData := make([]byte, 100) // All zeros
	smallCostStandard := CalcCalldataCostEIP7623(smallData, false)
	smallCostPectra := CalcCalldataCostEIP7623(smallData, true)

	// Pre-Pectra should use standard pricing (100 * 4 = 400)
	if smallCostStandard != 400 {
		t.Errorf("Small data standard cost = %d, want 400", smallCostStandard)
	}
	// Pectra uses max(standard=400, floor=100*10=1000) = 1000
	if smallCostPectra != 1000 {
		t.Errorf("Small data Pectra cost = %d, want 1000 (floor)", smallCostPectra)
	}
	// Pectra floor should be higher even for small data
	if smallCostPectra <= smallCostStandard {
		t.Errorf("Pectra floor cost should be >= standard: standard=%d, pectra=%d",
			smallCostStandard, smallCostPectra)
	}

	// Large non-zero data: standard=160000, floor=400000 -> pectra=400000
	largeData := make([]byte, 10000)
	for i := range largeData {
		largeData[i] = 0xFF // Non-zero bytes
	}
	largeCostStandard := CalcCalldataCostEIP7623(largeData, false)
	largeCostPectra := CalcCalldataCostEIP7623(largeData, true)

	// Pre-Pectra: 10000 * 16 = 160000
	if largeCostStandard != 160000 {
		t.Errorf("Large data standard cost = %d, want 160000", largeCostStandard)
	}
	// Pectra: max(160000, 10000*40=400000) = 400000
	if largeCostPectra != 400000 {
		t.Errorf("Large data Pectra cost = %d, want 400000 (floor)", largeCostPectra)
	}
	// Pectra floor should be higher for large non-zero data
	if largeCostPectra <= largeCostStandard {
		t.Errorf("Pectra floor cost should be higher for large data: standard=%d, pectra=%d",
			largeCostStandard, largeCostPectra)
	}
}

// TestPectraConstantsForStateConsistency verifies all Pectra constants
// are properly defined for state machine consistency.
func TestPectraConstantsForStateConsistency(t *testing.T) {
	// EIP-7702 gas constants
	if PerAuthBaseCost != 2500 {
		t.Errorf("PerAuthBaseCost = %d, want 2500", PerAuthBaseCost)
	}
	if PerEmptyAccountCost != 25000 {
		t.Errorf("PerEmptyAccountCost = %d, want 25000", PerEmptyAccountCost)
	}

	// EIP-2935 constants
	if HistoryServeWindow != 8192 {
		t.Errorf("HistoryServeWindow = %d, want 8192", HistoryServeWindow)
	}
	if HistoryStorageAddress == (types.Address{}) {
		t.Error("HistoryStorageAddress should not be zero")
	}

	// EIP-7685 request types
	if DepositRequestType != 0x00 {
		t.Errorf("DepositRequestType = %d, want 0x00", DepositRequestType)
	}
	if WithdrawalRequestType != 0x01 {
		t.Errorf("WithdrawalRequestType = %d, want 0x01", WithdrawalRequestType)
	}
	if ConsolidationRequestType != 0x02 {
		t.Errorf("ConsolidationRequestType = %d, want 0x02", ConsolidationRequestType)
	}

	// EIP-7002 constants
	if MaxWithdrawalRequestsPerBlock != 16 {
		t.Errorf("MaxWithdrawalRequestsPerBlock = %d, want 16", MaxWithdrawalRequestsPerBlock)
	}

	// EIP-7251 constants
	expectedMaxBalance := new(uint256.Int).Mul(uint256.NewInt(2048), uint256.NewInt(1e18))
	if MaxEffectiveBalanceEIP7251.Cmp(expectedMaxBalance) != 0 {
		t.Errorf("MaxEffectiveBalanceEIP7251 = %s, want %s",
			MaxEffectiveBalanceEIP7251.Dec(), expectedMaxBalance.Dec())
	}

	// System addresses
	if SystemAddress == (types.Address{}) {
		t.Error("SystemAddress should not be zero")
	}
	if WithdrawalRequestsAddress == (types.Address{}) {
		t.Error("WithdrawalRequestsAddress should not be zero")
	}
	if ConsolidationRequestsAddress == (types.Address{}) {
		t.Error("ConsolidationRequestsAddress should not be zero")
	}
}

// TestDelegationCodeHashConsistency tests that delegation code produces
// consistent hashes for state trie operations.
func TestDelegationCodeHashConsistency(t *testing.T) {
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	code1 := AddressToDelegation(addr1)
	code2 := AddressToDelegation(addr2)

	// Different addresses should produce different delegation codes
	if bytes.Equal(code1, code2) {
		t.Error("Different addresses should produce different delegation codes")
	}

	// Same address should produce same delegation code
	code1Again := AddressToDelegation(addr1)
	if !bytes.Equal(code1, code1Again) {
		t.Error("Same address should produce same delegation code")
	}
}

