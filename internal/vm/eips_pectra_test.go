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

	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// EIP-7702 Delegation Tests
// =============================================================================

func TestHasDelegation(t *testing.T) {
	tests := []struct {
		name   string
		code   []byte
		expect bool
	}{
		{
			name:   "empty code",
			code:   []byte{},
			expect: false,
		},
		{
			name:   "too short",
			code:   []byte{0xef, 0x01, 0x00},
			expect: false,
		},
		{
			name:   "wrong prefix",
			code:   append([]byte{0xef, 0x02, 0x00}, make([]byte, 20)...),
			expect: false,
		},
		{
			name:   "valid delegation",
			code:   append(DelegationPrefix, make([]byte, 20)...),
			expect: true,
		},
		{
			name:   "too long",
			code:   append(DelegationPrefix, make([]byte, 21)...),
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasDelegation(tt.code)
			if got != tt.expect {
				t.Errorf("HasDelegation() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestParseDelegation(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	delegationCode := AddressToDelegation(addr)

	tests := []struct {
		name      string
		code      []byte
		wantAddr  types.Address
		wantValid bool
	}{
		{
			name:      "empty code",
			code:      []byte{},
			wantAddr:  types.Address{},
			wantValid: false,
		},
		{
			name:      "valid delegation",
			code:      delegationCode,
			wantAddr:  addr,
			wantValid: true,
		},
		{
			name:      "invalid prefix",
			code:      append([]byte{0xff, 0x01, 0x00}, addr.Bytes()...),
			wantAddr:  types.Address{},
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAddr, gotValid := ParseDelegation(tt.code)
			if gotValid != tt.wantValid {
				t.Errorf("ParseDelegation() valid = %v, want %v", gotValid, tt.wantValid)
			}
			if gotAddr != tt.wantAddr {
				t.Errorf("ParseDelegation() addr = %v, want %v", gotAddr, tt.wantAddr)
			}
		})
	}
}

func TestAddressToDelegation(t *testing.T) {
	addr := types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01")
	code := AddressToDelegation(addr)

	// Verify prefix
	if !bytes.HasPrefix(code, DelegationPrefix) {
		t.Errorf("AddressToDelegation() should have delegation prefix")
	}

	// Verify length
	if len(code) != 23 {
		t.Errorf("AddressToDelegation() len = %d, want 23", len(code))
	}

	// Verify address bytes
	if !bytes.Equal(code[3:], addr.Bytes()) {
		t.Errorf("AddressToDelegation() address mismatch")
	}

	// Verify round-trip
	parsedAddr, valid := ParseDelegation(code)
	if !valid {
		t.Error("Round-trip: ParseDelegation() returned invalid")
	}
	if parsedAddr != addr {
		t.Errorf("Round-trip: got %v, want %v", parsedAddr, addr)
	}
}

func TestDelegationPrefixBytes(t *testing.T) {
	// Verify the delegation prefix is exactly 0xef0100
	if len(DelegationPrefix) != 3 {
		t.Errorf("DelegationPrefix length = %d, want 3", len(DelegationPrefix))
	}
	if DelegationPrefix[0] != 0xef || DelegationPrefix[1] != 0x01 || DelegationPrefix[2] != 0x00 {
		t.Errorf("DelegationPrefix = %x, want ef0100", DelegationPrefix)
	}
}

// =============================================================================
// EIP-2935 History Storage Tests
// =============================================================================

func TestHistoryStorageAddress(t *testing.T) {
	// Verify the history storage address matches EIP-2935 official specification
	// Address derived as: rlp([0xfffffffffffffffffffffffffffffffffffffffe, 0])
	expectedAddr := types.HexToAddress("0x0000F90827F1C53a10CB7A02335B175320002935")
	if HistoryStorageAddress != expectedAddr {
		t.Errorf("HistoryStorageAddress = %v, want %v", HistoryStorageAddress, expectedAddr)
	}
}

func TestHistoryServeWindow(t *testing.T) {
	// Verify the history serve window is 8192 per EIP-2935
	if HistoryServeWindow != 8192 {
		t.Errorf("HistoryServeWindow = %d, want 8192", HistoryServeWindow)
	}
}

func TestHistoryStorageSlotCalculation(t *testing.T) {
	// Test that slot calculation uses modulo correctly
	tests := []struct {
		blockNum     uint64
		expectedSlot uint64
	}{
		{0, 0},
		{1, 1},
		{8191, 8191},
		{8192, 0},    // Wraps around
		{8193, 1},    // Wraps around
		{16384, 0},   // 2x wrap
		{100000, 100000 % 8192},
	}

	for _, tt := range tests {
		slot := tt.blockNum % HistoryServeWindow
		if slot != tt.expectedSlot {
			t.Errorf("Block %d: slot = %d, want %d", tt.blockNum, slot, tt.expectedSlot)
		}
	}
}

func TestHistoryStorageCode(t *testing.T) {
	// Verify the history storage code is not empty
	if len(HistoryStorageCode) == 0 {
		t.Error("HistoryStorageCode should not be empty")
	}
}

// =============================================================================
// EIP-7251 Max Effective Balance Tests
// =============================================================================

func TestMaxEffectiveBalanceEIP7251(t *testing.T) {
	// Verify the max effective balance is 2048 ETH (2048 * 10^18 wei)
	// uint256 String() returns hex, so we compare with Dec()
	expectedDec := "2048000000000000000000"
	if MaxEffectiveBalanceEIP7251.Dec() != expectedDec {
		t.Errorf("MaxEffectiveBalanceEIP7251 = %s, want %s", MaxEffectiveBalanceEIP7251.Dec(), expectedDec)
	}
}

// =============================================================================
// EIP-7685 Request Types Tests
// =============================================================================

func TestRequestTypes(t *testing.T) {
	if DepositRequestType != 0x00 {
		t.Errorf("DepositRequestType = %d, want 0x00", DepositRequestType)
	}
	if WithdrawalRequestType != 0x01 {
		t.Errorf("WithdrawalRequestType = %d, want 0x01", WithdrawalRequestType)
	}
	if ConsolidationRequestType != 0x02 {
		t.Errorf("ConsolidationRequestType = %d, want 0x02", ConsolidationRequestType)
	}
}

func TestSystemAddresses(t *testing.T) {
	// Verify system addresses are not zero
	if SystemAddress == (types.Address{}) {
		t.Error("SystemAddress should not be zero")
	}
	if WithdrawalRequestsAddress == (types.Address{}) {
		t.Error("WithdrawalRequestsAddress should not be zero")
	}
	if ConsolidationRequestsAddress == (types.Address{}) {
		t.Error("ConsolidationRequestsAddress should not be zero")
	}
	if DepositContractAddress == (types.Address{}) {
		t.Error("DepositContractAddress should not be zero")
	}
}

// =============================================================================
// EIP-2537 BLS Precompile Addresses Tests
// =============================================================================

func TestBLSPrecompileAddresses(t *testing.T) {
	// Verify BLS precompile addresses are sequential from 0x0b to 0x13
	expectedAddrs := []struct {
		name string
		addr types.Address
		byte byte
	}{
		{"BLS12G1AddAddr", BLS12G1AddAddr, 0x0b},
		{"BLS12G1MulAddr", BLS12G1MulAddr, 0x0c},
		{"BLS12G1MultiExpAddr", BLS12G1MultiExpAddr, 0x0d},
		{"BLS12G2AddAddr", BLS12G2AddAddr, 0x0e},
		{"BLS12G2MulAddr", BLS12G2MulAddr, 0x0f},
		{"BLS12G2MultiExpAddr", BLS12G2MultiExpAddr, 0x10},
		{"BLS12PairingAddr", BLS12PairingAddr, 0x11},
		{"BLS12MapG1Addr", BLS12MapG1Addr, 0x12},
		{"BLS12MapG2Addr", BLS12MapG2Addr, 0x13},
	}

	for _, tc := range expectedAddrs {
		expected := types.BytesToAddress([]byte{tc.byte})
		if tc.addr != expected {
			t.Errorf("%s = %v, want %v", tc.name, tc.addr, expected)
		}
	}
}

// =============================================================================
// Gas Calculation Tests
// =============================================================================

func TestCalcAuthorizationGas(t *testing.T) {
	tests := []struct {
		authCount       int
		newAccountCount int
		expectedGas     uint64
	}{
		{0, 0, 0},
		{1, 0, PerAuthBaseCost},
		{0, 1, PerEmptyAccountCost},
		{1, 1, PerAuthBaseCost + PerEmptyAccountCost},
		{10, 5, 10*PerAuthBaseCost + 5*PerEmptyAccountCost},
	}

	for _, tt := range tests {
		got := CalcAuthorizationGas(tt.authCount, tt.newAccountCount)
		if got != tt.expectedGas {
			t.Errorf("CalcAuthorizationGas(%d, %d) = %d, want %d",
				tt.authCount, tt.newAccountCount, got, tt.expectedGas)
		}
	}
}

func TestPerAuthBaseCost(t *testing.T) {
	if PerAuthBaseCost != 2500 {
		t.Errorf("PerAuthBaseCost = %d, want 2500", PerAuthBaseCost)
	}
}

func TestPerEmptyAccountCost(t *testing.T) {
	if PerEmptyAccountCost != 25000 {
		t.Errorf("PerEmptyAccountCost = %d, want 25000", PerEmptyAccountCost)
	}
}

// =============================================================================
// Pectra Instruction Set Tests
// =============================================================================

func TestNewPectraInstructionSet(t *testing.T) {
	jt := newPectraInstructionSet()

	// Verify the instruction set is not nil
	for i := 0; i < 256; i++ {
		if jt[i] == nil {
			t.Errorf("Pectra instruction set op 0x%x is nil", i)
		}
	}
}

func TestPectraInstructionSetIncludesPrague(t *testing.T) {
	pectra := newPectraInstructionSet()
	prague := newPragueInstructionSet()

	// Verify that all Prague instructions are present in Pectra
	for i := 0; i < 256; i++ {
		if prague[i] != nil && pectra[i] == nil {
			t.Errorf("Pectra missing Prague op 0x%x", i)
		}
	}
}

// =============================================================================
// EIP-6110 Deposit Request Tests
// =============================================================================

func TestDepositRequestSerialize(t *testing.T) {
	deposit := &DepositRequest{
		Amount: 32000000000, // 32 Gwei
		Index:  12345,
	}
	copy(deposit.Pubkey[:], bytes.Repeat([]byte{0xaa}, 48))
	copy(deposit.WithdrawalCredentials[:], bytes.Repeat([]byte{0xbb}, 32))
	copy(deposit.Signature[:], bytes.Repeat([]byte{0xcc}, 96))

	serialized := deposit.Serialize()
	if len(serialized) != DepositRequestSize {
		t.Errorf("Serialized deposit size = %d, want %d", len(serialized), DepositRequestSize)
	}

	// Verify pubkey
	for i := 0; i < 48; i++ {
		if serialized[i] != 0xaa {
			t.Errorf("Pubkey byte %d = %x, want 0xaa", i, serialized[i])
		}
	}
}

func TestDepositEventSignature(t *testing.T) {
	// Verify the deposit event signature is not zero
	if DepositEventSignature == (types.Hash{}) {
		t.Error("DepositEventSignature should not be zero")
	}
}

func TestDepositContractAddress(t *testing.T) {
	// Verify the deposit contract address matches the canonical address
	expectedAddr := types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")
	if DepositContractAddress != expectedAddr {
		t.Errorf("DepositContractAddress = %v, want %v", DepositContractAddress, expectedAddr)
	}
}

// =============================================================================
// EIP-7002 Withdrawal Request Tests
// =============================================================================

func TestWithdrawalRequestSerialize(t *testing.T) {
	req := &WithdrawalRequest{
		SourceAddress: types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Amount:        1000000000, // 1 Gwei
	}
	copy(req.ValidatorPubkey[:], bytes.Repeat([]byte{0xdd}, 48))

	serialized := req.Serialize()
	if len(serialized) != WithdrawalRequestSize {
		t.Errorf("Serialized withdrawal request size = %d, want %d", len(serialized), WithdrawalRequestSize)
	}
}

func TestWithdrawalRequestRoundTrip(t *testing.T) {
	original := &WithdrawalRequest{
		SourceAddress: types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01"),
		Amount:        500000000, // 0.5 Gwei
	}
	copy(original.ValidatorPubkey[:], bytes.Repeat([]byte{0xee}, 48))

	serialized := original.Serialize()
	decoded, err := DeserializeWithdrawalRequest(serialized)
	if err != nil {
		t.Fatalf("DeserializeWithdrawalRequest error: %v", err)
	}

	if decoded.SourceAddress != original.SourceAddress {
		t.Errorf("SourceAddress mismatch: got %v, want %v", decoded.SourceAddress, original.SourceAddress)
	}
	if decoded.ValidatorPubkey != original.ValidatorPubkey {
		t.Errorf("ValidatorPubkey mismatch")
	}
	if decoded.Amount != original.Amount {
		t.Errorf("Amount mismatch: got %d, want %d", decoded.Amount, original.Amount)
	}
}

func TestWithdrawalRequestsAddress(t *testing.T) {
	// Verify the address is not zero
	if WithdrawalRequestsAddress == (types.Address{}) {
		t.Error("WithdrawalRequestsAddress should not be zero")
	}
}

func TestMaxWithdrawalRequestsPerBlock(t *testing.T) {
	if MaxWithdrawalRequestsPerBlock != 16 {
		t.Errorf("MaxWithdrawalRequestsPerBlock = %d, want 16", MaxWithdrawalRequestsPerBlock)
	}
}

// =============================================================================
// EIP-7251 Consolidation Request Tests
// =============================================================================

func TestConsolidationRequestSerialize(t *testing.T) {
	req := &ConsolidationRequest{
		SourceAddress: types.HexToAddress("0x1234567890123456789012345678901234567890"),
	}
	copy(req.SourcePubkey[:], bytes.Repeat([]byte{0x11}, 48))
	copy(req.TargetPubkey[:], bytes.Repeat([]byte{0x22}, 48))

	serialized := req.Serialize()
	if len(serialized) != ConsolidationRequestSize {
		t.Errorf("Serialized consolidation request size = %d, want %d", len(serialized), ConsolidationRequestSize)
	}
}

func TestConsolidationRequestRoundTrip(t *testing.T) {
	original := &ConsolidationRequest{
		SourceAddress: types.HexToAddress("0xfedcba9876543210fedcba9876543210fedcba98"),
	}
	copy(original.SourcePubkey[:], bytes.Repeat([]byte{0x33}, 48))
	copy(original.TargetPubkey[:], bytes.Repeat([]byte{0x44}, 48))

	serialized := original.Serialize()
	decoded, err := DeserializeConsolidationRequest(serialized)
	if err != nil {
		t.Fatalf("DeserializeConsolidationRequest error: %v", err)
	}

	if decoded.SourceAddress != original.SourceAddress {
		t.Errorf("SourceAddress mismatch")
	}
	if decoded.SourcePubkey != original.SourcePubkey {
		t.Errorf("SourcePubkey mismatch")
	}
	if decoded.TargetPubkey != original.TargetPubkey {
		t.Errorf("TargetPubkey mismatch")
	}
}

func TestMaxEffectiveBalanceElectra(t *testing.T) {
	// Verify the max effective balance is 2048 ETH
	expectedDec := "2048000000000000000000"
	if MaxEffectiveBalanceElectra.Dec() != expectedDec {
		t.Errorf("MaxEffectiveBalanceElectra = %s, want %s", MaxEffectiveBalanceElectra.Dec(), expectedDec)
	}
}

func TestMinActivationBalance(t *testing.T) {
	// Verify the min activation balance is 32 ETH
	expectedDec := "32000000000000000000"
	if MinActivationBalance.Dec() != expectedDec {
		t.Errorf("MinActivationBalance = %s, want %s", MinActivationBalance.Dec(), expectedDec)
	}
}

func TestConsolidationRequestsAddress(t *testing.T) {
	// Verify the address is not zero
	if ConsolidationRequestsAddress == (types.Address{}) {
		t.Error("ConsolidationRequestsAddress should not be zero")
	}
}

// =============================================================================
// EIP-7685 Execution Request Tests
// =============================================================================

func TestEncodeDecodeExecutionRequest(t *testing.T) {
	testData := []byte{0x01, 0x02, 0x03, 0x04}
	
	// Test deposit request type
	encoded := EncodeExecutionRequest(DepositRequestType, testData)
	if encoded[0] != DepositRequestType {
		t.Errorf("Encoded type = %d, want %d", encoded[0], DepositRequestType)
	}

	reqType, data, err := DecodeExecutionRequest(encoded)
	if err != nil {
		t.Fatalf("DecodeExecutionRequest error: %v", err)
	}
	if reqType != DepositRequestType {
		t.Errorf("Decoded type = %d, want %d", reqType, DepositRequestType)
	}
	if !bytes.Equal(data, testData) {
		t.Errorf("Decoded data mismatch")
	}
}

func TestDecodeEmptyRequest(t *testing.T) {
	_, _, err := DecodeExecutionRequest([]byte{})
	if err == nil {
		t.Error("DecodeExecutionRequest should fail on empty data")
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkHasDelegation(b *testing.B) {
	code := AddressToDelegation(types.HexToAddress("0x1234567890123456789012345678901234567890"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HasDelegation(code)
	}
}

func BenchmarkParseDelegation(b *testing.B) {
	code := AddressToDelegation(types.HexToAddress("0x1234567890123456789012345678901234567890"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseDelegation(code)
	}
}

func BenchmarkAddressToDelegation(b *testing.B) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AddressToDelegation(addr)
	}
}

func BenchmarkCalcAuthorizationGas(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcAuthorizationGas(10, 5)
	}
}

func BenchmarkNewPectraInstructionSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		newPectraInstructionSet()
	}
}

