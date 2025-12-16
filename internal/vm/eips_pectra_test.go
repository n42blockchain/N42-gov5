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
	// Verify the history storage address is not zero
	if HistoryStorageAddress == (types.Address{}) {
		t.Error("HistoryStorageAddress should not be zero")
	}
}

func TestHistoryServeWindow(t *testing.T) {
	// Verify the history serve window is 8192
	if HistoryServeWindow != 8192 {
		t.Errorf("HistoryServeWindow = %d, want 8192", HistoryServeWindow)
	}
}

// =============================================================================
// EIP-7251 Max Effective Balance Tests
// =============================================================================

func TestMaxEffectiveBalanceEIP7251(t *testing.T) {
	// Verify the max effective balance is 2048 ETH (2048 * 10^18 wei)
	expectedStr := "2048000000000000000000"
	if MaxEffectiveBalanceEIP7251.String() != expectedStr {
		t.Errorf("MaxEffectiveBalanceEIP7251 = %s, want %s", MaxEffectiveBalanceEIP7251.String(), expectedStr)
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

