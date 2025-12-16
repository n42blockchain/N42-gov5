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

// Tests adapted from go-ethereum and erigon VM test suites.

package vm

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Contract Tests (Reference: go-ethereum/core/vm/contract_test.go)
// =============================================================================

func TestAccountRef(t *testing.T) {
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	ref := AccountRef(addr)

	if ref.Address() != addr {
		t.Errorf("AccountRef.Address() = %v, want %v", ref.Address(), addr)
	}

	t.Logf("✓ AccountRef correctly wraps address")
}

func TestNewContract(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	value := uint256.NewInt(1000)
	gas := uint64(21000)

	contract := NewContract(caller, object, value, gas, false)

	if contract == nil {
		t.Fatal("NewContract returned nil")
	}
	if contract.CallerAddress != caller.Address() {
		t.Errorf("CallerAddress mismatch: got %v, want %v", contract.CallerAddress, caller.Address())
	}
	if contract.Gas != gas {
		t.Errorf("Gas mismatch: got %d, want %d", contract.Gas, gas)
	}
	if contract.Value().Cmp(value) != 0 {
		t.Errorf("Value mismatch: got %v, want %v", contract.Value(), value)
	}

	t.Logf("✓ NewContract creates contract correctly")
}

func TestContractUseGas(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	tests := []struct {
		name      string
		gas       uint64
		expected  bool
		remaining uint64
	}{
		{"use_small_amount", 100, true, 900},
		{"use_more", 200, true, 700},
		{"use_remaining", 700, true, 0},
		{"use_too_much", 1, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contract.UseGas(tt.gas)
			if result != tt.expected {
				t.Errorf("UseGas(%d) = %v, want %v", tt.gas, result, tt.expected)
			}
			if contract.Gas != tt.remaining {
				t.Errorf("Remaining gas = %d, want %d", contract.Gas, tt.remaining)
			}
		})
	}

	t.Logf("✓ Contract.UseGas works correctly")
}

func TestContractUseGasExact(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	// Use exactly all gas
	result := contract.UseGas(1000)
	if !result {
		t.Error("UseGas should succeed when using exactly available gas")
	}
	if contract.Gas != 0 {
		t.Errorf("Gas should be 0 after using all, got %d", contract.Gas)
	}

	t.Logf("✓ Contract.UseGas handles exact amount correctly")
}

func TestContractGetOp(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	// Set some bytecode
	code := []byte{byte(PUSH1), 0x60, byte(PUSH1), 0x40, byte(ADD), byte(STOP)}
	contract.Code = code

	tests := []struct {
		n        uint64
		expected OpCode
	}{
		{0, PUSH1},
		{2, PUSH1},
		{4, ADD},
		{5, STOP},
		{100, STOP}, // Beyond code length returns STOP
	}

	for _, tt := range tests {
		result := contract.GetOp(tt.n)
		if result != tt.expected {
			t.Errorf("GetOp(%d) = %v, want %v", tt.n, result, tt.expected)
		}
	}

	t.Logf("✓ Contract.GetOp returns correct opcodes")
}

func TestContractAddress(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	if contract.Address() != object.Address() {
		t.Errorf("Address() = %v, want %v", contract.Address(), object.Address())
	}

	t.Logf("✓ Contract.Address returns correct address")
}

func TestContractCaller(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	if contract.Caller() != caller.Address() {
		t.Errorf("Caller() = %v, want %v", contract.Caller(), caller.Address())
	}

	t.Logf("✓ Contract.Caller returns correct address")
}

func TestContractValue(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	value := uint256.NewInt(12345678)
	contract := NewContract(caller, object, value, 1000, false)

	if contract.Value().Cmp(value) != 0 {
		t.Errorf("Value() = %v, want %v", contract.Value(), value)
	}

	t.Logf("✓ Contract.Value returns correct value")
}

func TestContractSetCallCode(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	codeAddr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	code := []byte{byte(PUSH1), 0x00, byte(STOP)}

	contract.SetCallCode(&codeAddr, codeHash, code)

	if *contract.CodeAddr != codeAddr {
		t.Errorf("CodeAddr mismatch: got %v, want %v", *contract.CodeAddr, codeAddr)
	}
	if contract.CodeHash != codeHash {
		t.Errorf("CodeHash mismatch: got %v, want %v", contract.CodeHash, codeHash)
	}
	if string(contract.Code) != string(code) {
		t.Errorf("Code mismatch: got %x, want %x", contract.Code, code)
	}

	t.Logf("✓ Contract.SetCallCode works correctly")
}

func TestContractAsDelegate(t *testing.T) {
	callerAddr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	caller := AccountRef(callerAddr)
	parentAddr := types.HexToAddress("0x2222222222222222222222222222222222222222")
	parentValue := uint256.NewInt(100)

	// Create parent contract
	parentContract := NewContract(caller, AccountRef(parentAddr), parentValue, 1000, false)
	parentContract.CallerAddress = callerAddr

	// Create delegate contract
	delegateAddr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	delegateContract := NewContract(parentContract, AccountRef(delegateAddr), uint256.NewInt(0), 500, false)

	// Apply delegate call
	delegateContract.AsDelegate()

	// After AsDelegate, CallerAddress and value should be from parent
	if delegateContract.CallerAddress != callerAddr {
		t.Errorf("After AsDelegate, CallerAddress = %v, want %v", delegateContract.CallerAddress, callerAddr)
	}
	if delegateContract.Value().Cmp(parentValue) != 0 {
		t.Errorf("After AsDelegate, Value = %v, want %v", delegateContract.Value(), parentValue)
	}

	t.Logf("✓ Contract.AsDelegate sets caller and value from parent")
}

func TestContractJumpdests(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	// New contract should have initialized jumpdests map
	if contract.jumpdests == nil {
		t.Error("jumpdests should be initialized")
	}

	t.Logf("✓ Contract jumpdests initialized correctly")
}

func TestContractJumpdestsInheritance(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	parentAddr := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// Create parent contract
	parentContract := NewContract(caller, AccountRef(parentAddr), uint256.NewInt(0), 1000, false)
	parentContract.jumpdests[types.HexToHash("0xdead")] = []uint64{1, 2, 3}

	// Create child contract with parent as caller
	childAddr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	childContract := NewContract(parentContract, AccountRef(childAddr), uint256.NewInt(0), 500, false)

	// Child should inherit parent's jumpdests (same reference)
	// Add a key to parent and check if child sees it
	testHash := types.HexToHash("0xtest")
	parentContract.jumpdests[testHash] = []uint64{10, 20, 30}
	if childContract.jumpdests[testHash] == nil {
		t.Error("Child contract should share parent's jumpdests map")
	}

	t.Logf("✓ Contract jumpdests inherited from parent contract")
}

func TestContractSkipAnalysis(t *testing.T) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))

	contract := NewContract(caller, object, uint256.NewInt(0), 1000, true)

	if !contract.skipAnalysis {
		t.Error("skipAnalysis should be true")
	}

	contract2 := NewContract(caller, object, uint256.NewInt(0), 1000, false)

	if contract2.skipAnalysis {
		t.Error("skipAnalysis should be false")
	}

	t.Logf("✓ Contract skipAnalysis flag set correctly")
}

// =============================================================================
// Contract Benchmark Tests
// =============================================================================

func BenchmarkNewContract(b *testing.B) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	value := uint256.NewInt(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NewContract(caller, object, value, 21000, false)
	}
}

func BenchmarkContractUseGas(b *testing.B) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(caller, object, uint256.NewInt(0), 1000000, false)
		for j := 0; j < 100; j++ {
			contract.UseGas(1)
		}
	}
}

func BenchmarkContractGetOp(b *testing.B) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)
	contract.Code = make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract.GetOp(uint64(i % 1024))
	}
}

func BenchmarkContractSetCallCode(b *testing.B) {
	caller := AccountRef(types.HexToAddress("0x1111111111111111111111111111111111111111"))
	object := AccountRef(types.HexToAddress("0x2222222222222222222222222222222222222222"))
	codeAddr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	code := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)
		contract.SetCallCode(&codeAddr, codeHash, code)
	}
}

