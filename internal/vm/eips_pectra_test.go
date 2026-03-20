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
	"github.com/n42blockchain/N42/params"
)

// EIP-7702 Delegation Tests

func TestHasDelegation(t *testing.T) {
	tests := []struct {
		name   string
		code   []byte
		expect bool
	}{
		{"empty code", []byte{}, false},
		{"too short", []byte{0xef, 0x01, 0x00}, false},
		{"wrong prefix", append([]byte{0xef, 0x02, 0x00}, make([]byte, 20)...), false},
		{"valid delegation", append(DelegationPrefix, make([]byte, 20)...), true},
		{"too long", append(DelegationPrefix, make([]byte, 21)...), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasDelegation(tt.code); got != tt.expect {
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
		{"nil code", nil, types.Address{}, false},
		{"empty code", []byte{}, types.Address{}, false},
		{"too short", []byte{0xef, 0x01}, types.Address{}, false},
		{"valid delegation", delegationCode, addr, true},
		{"wrong prefix byte 0", append([]byte{0xff, 0x01, 0x00}, addr.Bytes()...), types.Address{}, false},
		{"wrong prefix byte 1", append([]byte{0xef, 0x02, 0x00}, addr.Bytes()...), types.Address{}, false},
		{"wrong prefix byte 2", append([]byte{0xef, 0x01, 0x01}, addr.Bytes()...), types.Address{}, false},
		{"too long", append(DelegationPrefix, make([]byte, 21)...), types.Address{}, false},
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

	if !bytes.HasPrefix(code, DelegationPrefix) {
		t.Error("AddressToDelegation() should have delegation prefix")
	}
	if len(code) != 23 {
		t.Errorf("AddressToDelegation() len = %d, want 23", len(code))
	}
	if !bytes.Equal(code[3:], addr.Bytes()) {
		t.Error("AddressToDelegation() address mismatch")
	}

	// Round-trip
	parsedAddr, valid := ParseDelegation(code)
	if !valid {
		t.Error("Round-trip: ParseDelegation() returned invalid")
	}
	if parsedAddr != addr {
		t.Errorf("Round-trip: got %v, want %v", parsedAddr, addr)
	}
}

func TestDelegationPrefixBytes(t *testing.T) {
	if len(DelegationPrefix) != 3 {
		t.Errorf("DelegationPrefix length = %d, want 3", len(DelegationPrefix))
	}
	if DelegationPrefix[0] != 0xef || DelegationPrefix[1] != 0x01 || DelegationPrefix[2] != 0x00 {
		t.Errorf("DelegationPrefix = %x, want ef0100", DelegationPrefix)
	}
}

// EIP-2935 History Storage Tests

func TestHistoryStorageAddress(t *testing.T) {
	expectedAddr := types.HexToAddress("0x0000F90827F1C53a10CB7A02335B175320002935")
	if HistoryStorageAddress != expectedAddr {
		t.Errorf("HistoryStorageAddress = %v, want %v", HistoryStorageAddress, expectedAddr)
	}
}

func TestHistoryServeWindow(t *testing.T) {
	if HistoryServeWindow != 8192 {
		t.Errorf("HistoryServeWindow = %d, want 8192", HistoryServeWindow)
	}
}

func TestHistoryStorageSlotCalculation(t *testing.T) {
	tests := []struct {
		blockNum     uint64
		expectedSlot uint64
	}{
		{0, 0},
		{1, 1},
		{8191, 8191},
		{8192, 0},
		{8193, 1},
		{16384, 0},
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
	if len(HistoryStorageCode) == 0 {
		t.Error("HistoryStorageCode should not be empty")
	}
}

// EIP-7251 Max Effective Balance Tests

func TestMaxEffectiveBalanceEIP7251(t *testing.T) {
	expectedDec := "2048000000000000000000"
	if MaxEffectiveBalanceEIP7251.Dec() != expectedDec {
		t.Errorf("MaxEffectiveBalanceEIP7251 = %s, want %s", MaxEffectiveBalanceEIP7251.Dec(), expectedDec)
	}
}

// EIP-7685 Request Types Tests

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
	addresses := []struct {
		name string
		addr types.Address
		want types.Address
	}{
		{"SystemAddress", SystemAddress, types.HexToAddress("0xfffffffffffffffffffffffffffffffffffffffe")},
		{"WithdrawalRequestsAddress", WithdrawalRequestsAddress, types.HexToAddress("0x00000961EF480EB55E80D19AD83579A64C007002")},
		{"ConsolidationRequestsAddress", ConsolidationRequestsAddress, types.HexToAddress("0x0000BBDDC7CE488642FB579F8B00F3A590007251")},
		{"DepositContractAddress", DepositContractAddress, types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")},
	}

	for _, a := range addresses {
		if a.addr == (types.Address{}) {
			t.Errorf("%s should not be zero", a.name)
		}
		if a.addr != a.want {
			t.Errorf("%s = %v, want %v", a.name, a.addr, a.want)
		}
	}
}

// EIP-2537 BLS Precompile Addresses Tests

func TestBLSPrecompileAddresses(t *testing.T) {
	expectedAddrs := []struct {
		name string
		addr types.Address
		byte byte
	}{
		{"BLS12G1AddAddr", BLS12G1AddAddr, 0x0b},
		{"BLS12G1MulAddr", BLS12G1MulAddr, 0x0c},
		{"BLS12G1MultiExpAddr", BLS12G1MultiExpAddr, 0x0c},
		{"BLS12G2AddAddr", BLS12G2AddAddr, 0x0d},
		{"BLS12G2MulAddr", BLS12G2MulAddr, 0x0e},
		{"BLS12G2MultiExpAddr", BLS12G2MultiExpAddr, 0x0e},
		{"BLS12PairingAddr", BLS12PairingAddr, 0x0f},
		{"BLS12MapG1Addr", BLS12MapG1Addr, 0x10},
		{"BLS12MapG2Addr", BLS12MapG2Addr, 0x11},
	}

	for _, tc := range expectedAddrs {
		expected := types.BytesToAddress([]byte{tc.byte})
		if tc.addr != expected {
			t.Errorf("%s = %v, want %v", tc.name, tc.addr, expected)
		}
	}
}

func TestActiveBLSPrecompileLayoutMatchesCurrentForkRules(t *testing.T) {
	activeAddrs := []types.Address{
		types.BytesToAddress([]byte{0x0b}),
		types.BytesToAddress([]byte{0x0c}),
		types.BytesToAddress([]byte{0x0d}),
		types.BytesToAddress([]byte{0x0e}),
		types.BytesToAddress([]byte{0x0f}),
		types.BytesToAddress([]byte{0x10}),
		types.BytesToAddress([]byte{0x11}),
	}
	inactiveAddrs := []types.Address{
		types.BytesToAddress([]byte{0x12}),
		types.BytesToAddress([]byte{0x13}),
		types.BytesToAddress([]byte{0x14}),
		types.BytesToAddress([]byte{0x15}),
		types.BytesToAddress([]byte{0x16}),
		types.BytesToAddress([]byte{0x17}),
	}

	testCases := []struct {
		name      string
		contracts map[types.Address]PrecompiledContract
		rules     *params.Rules
	}{
		{"Prague", PrecompiledContractsPrague, &params.Rules{IsPrague: true}},
		{"Osaka", PrecompiledContractsOsaka, &params.Rules{IsOsaka: true}},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, addr := range activeAddrs {
				if _, ok := tc.contracts[addr]; !ok {
					t.Fatalf("expected active BLS precompile at %s", addr.Hex())
				}
			}
			for _, addr := range inactiveAddrs {
				if _, ok := tc.contracts[addr]; ok {
					t.Fatalf("unexpected inactive precompile at %s", addr.Hex())
				}
			}

			activeSet := make(map[types.Address]struct{})
			for _, addr := range ActivePrecompiles(tc.rules) {
				activeSet[addr] = struct{}{}
			}
			for _, addr := range activeAddrs {
				if _, ok := activeSet[addr]; !ok {
					t.Fatalf("expected warmed precompile at %s", addr.Hex())
				}
			}
			for _, addr := range inactiveAddrs {
				if _, ok := activeSet[addr]; ok {
					t.Fatalf("unexpected warmed inactive precompile at %s", addr.Hex())
				}
			}
		})
	}
}

func TestBLS12381MultiExpGasMatchesSpec(t *testing.T) {
	testCases := []struct {
		name       string
		precompile PrecompiledContract
		pairSize   int
		gasByK     map[int]uint64
	}{
		{
			name:       "G1MSM",
			precompile: GetBls12381G1MultiExp(),
			pairSize:   160,
			gasByK: map[int]uint64{
				1:   12000,
				2:   22776,
				3:   30528,
				8:   69888,
				64:  442368,
				128: 797184,
				149: 927972,
			},
		},
		{
			name:       "G2MSM",
			precompile: GetBls12381G2MultiExp(),
			pairSize:   288,
			gasByK: map[int]uint64{
				1:   22500,
				2:   45000,
				3:   62302,
				8:   143280,
				64:  838080,
				128: 1509120,
				149: 1756710,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for k, want := range tc.gasByK {
				input := make([]byte, k*tc.pairSize)
				if got := tc.precompile.RequiredGas(input); got != want {
					t.Fatalf("RequiredGas(%d pairs)=%d, want %d", k, got, want)
				}
			}
		})
	}
}

func TestBLS12381PairingGasMatchesSpec(t *testing.T) {
	p := GetBls12381Pairing()
	testCases := map[int]uint64{
		1: 70300,
		2: 102900,
		3: 135500,
	}

	for pairs, want := range testCases {
		input := make([]byte, pairs*384)
		if got := p.RequiredGas(input); got != want {
			t.Fatalf("RequiredGas(%d pairs)=%d, want %d", pairs, got, want)
		}
	}

	if params.Bls12381PairingBaseGas != 37700 {
		t.Fatalf("Bls12381PairingBaseGas=%d, want 37700", params.Bls12381PairingBaseGas)
	}
	if params.Bls12381PairingPerPairGas != 32600 {
		t.Fatalf("Bls12381PairingPerPairGas=%d, want 32600", params.Bls12381PairingPerPairGas)
	}
}

// Gas Calculation Tests

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

// Pectra Instruction Set Tests

func TestNewPectraInstructionSet(t *testing.T) {
	jt := newPectraInstructionSet()

	for i := 0; i < 256; i++ {
		if jt[i] == nil {
			t.Errorf("Pectra instruction set op 0x%x is nil", i)
		}
	}
}

func TestPectraInstructionSetIncludesPrague(t *testing.T) {
	pectra := newPectraInstructionSet()
	prague := newPragueInstructionSet()

	for i := 0; i < 256; i++ {
		if prague[i] != nil && pectra[i] == nil {
			t.Errorf("Pectra missing Prague op 0x%x", i)
		}
	}
}

// EIP-6110 Deposit Request Tests

func TestDepositRequestSerialize(t *testing.T) {
	deposit := &DepositRequest{
		Amount: 32000000000,
		Index:  12345,
	}
	copy(deposit.Pubkey[:], bytes.Repeat([]byte{0xaa}, 48))
	copy(deposit.WithdrawalCredentials[:], bytes.Repeat([]byte{0xbb}, 32))
	copy(deposit.Signature[:], bytes.Repeat([]byte{0xcc}, 96))

	serialized := deposit.Serialize()
	if len(serialized) != DepositRequestSize {
		t.Errorf("Serialized deposit size = %d, want %d", len(serialized), DepositRequestSize)
	}

	for i := 0; i < 48; i++ {
		if serialized[i] != 0xaa {
			t.Errorf("Pubkey byte %d = %x, want 0xaa", i, serialized[i])
			break
		}
	}
}

func TestDepositEventSignature(t *testing.T) {
	if DepositEventSignature == (types.Hash{}) {
		t.Error("DepositEventSignature should not be zero")
	}
}

func TestDepositContractAddress(t *testing.T) {
	expectedAddr := types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")
	if DepositContractAddress != expectedAddr {
		t.Errorf("DepositContractAddress = %v, want %v", DepositContractAddress, expectedAddr)
	}
}

// EIP-7002 Withdrawal Request Tests

func TestWithdrawalRequestRoundTrip(t *testing.T) {
	original := &WithdrawalRequest{
		SourceAddress: types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01"),
		Amount:        500000000,
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
		t.Error("ValidatorPubkey mismatch")
	}
	if decoded.Amount != original.Amount {
		t.Errorf("Amount = %d, want %d", decoded.Amount, original.Amount)
	}
}

func TestMaxWithdrawalRequestsPerBlock(t *testing.T) {
	if MaxWithdrawalRequestsPerBlock != 16 {
		t.Errorf("MaxWithdrawalRequestsPerBlock = %d, want 16", MaxWithdrawalRequestsPerBlock)
	}
}

// EIP-7251 Consolidation Request Tests

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
		t.Error("SourceAddress mismatch")
	}
	if decoded.SourcePubkey != original.SourcePubkey {
		t.Error("SourcePubkey mismatch")
	}
	if decoded.TargetPubkey != original.TargetPubkey {
		t.Error("TargetPubkey mismatch")
	}
}

func TestMaxEffectiveBalanceElectra(t *testing.T) {
	expectedDec := "2048000000000000000000"
	if MaxEffectiveBalanceElectra.Dec() != expectedDec {
		t.Errorf("MaxEffectiveBalanceElectra = %s, want %s", MaxEffectiveBalanceElectra.Dec(), expectedDec)
	}
}

func TestMinActivationBalance(t *testing.T) {
	expectedDec := "32000000000000000000"
	if MinActivationBalance.Dec() != expectedDec {
		t.Errorf("MinActivationBalance = %s, want %s", MinActivationBalance.Dec(), expectedDec)
	}
}

// EIP-7685 Execution Request Tests

func TestEncodeDecodeExecutionRequest(t *testing.T) {
	requestTypes := []struct {
		name    string
		reqType byte
		data    []byte
	}{
		{"deposit", DepositRequestType, []byte{0x01, 0x02, 0x03}},
		{"withdrawal", WithdrawalRequestType, []byte{0x04, 0x05, 0x06}},
		{"consolidation", ConsolidationRequestType, []byte{0x07, 0x08, 0x09}},
	}

	for _, rt := range requestTypes {
		t.Run(rt.name, func(t *testing.T) {
			encoded := EncodeExecutionRequest(rt.reqType, rt.data)
			if encoded[0] != rt.reqType {
				t.Errorf("Encoded type = %d, want %d", encoded[0], rt.reqType)
			}

			decodedType, decodedData, err := DecodeExecutionRequest(encoded)
			if err != nil {
				t.Fatalf("DecodeExecutionRequest error: %v", err)
			}
			if decodedType != rt.reqType {
				t.Errorf("Decoded type = %d, want %d", decodedType, rt.reqType)
			}
			if !bytes.Equal(decodedData, rt.data) {
				t.Error("Decoded data mismatch")
			}
		})
	}
}

func TestDecodeEmptyRequest(t *testing.T) {
	_, _, err := DecodeExecutionRequest([]byte{})
	if err == nil {
		t.Error("DecodeExecutionRequest should fail on empty data")
	}
}

// Benchmarks

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
