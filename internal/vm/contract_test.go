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

var (
	testCallerAddr   = types.HexToAddress("0x1111111111111111111111111111111111111111")
	testObjectAddr   = types.HexToAddress("0x2222222222222222222222222222222222222222")
	testDelegateAddr = types.HexToAddress("0x3333333333333333333333333333333333333333")
)

func newTestContract(gas uint64) *Contract {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)
	return NewContract(caller, object, uint256.NewInt(0), gas, false)
}

func TestAccountRef(t *testing.T) {
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	ref := AccountRef(addr)

	if ref.Address() != addr {
		t.Errorf("AccountRef.Address() = %v, want %v", ref.Address(), addr)
	}
}

func TestNewContract(t *testing.T) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)
	value := uint256.NewInt(1000)
	gas := uint64(21000)

	contract := NewContract(caller, object, value, gas, false)

	if contract == nil {
		t.Fatal("NewContract returned nil")
	}
	if contract.CallerAddress != caller.Address() {
		t.Errorf("CallerAddress = %v, want %v", contract.CallerAddress, caller.Address())
	}
	if contract.Gas != gas {
		t.Errorf("Gas = %d, want %d", contract.Gas, gas)
	}
	if contract.Value().Cmp(value) != 0 {
		t.Errorf("Value = %v, want %v", contract.Value(), value)
	}
}

func TestContractUseGas(t *testing.T) {
	contract := newTestContract(1000)

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
				t.Errorf("remaining gas = %d, want %d", contract.Gas, tt.remaining)
			}
		})
	}
}

func TestContractUseGasExact(t *testing.T) {
	contract := newTestContract(1000)

	if !contract.UseGas(1000) {
		t.Error("UseGas should succeed when using exactly available gas")
	}
	if contract.Gas != 0 {
		t.Errorf("Gas should be 0 after using all, got %d", contract.Gas)
	}
}

func TestContractGetOp(t *testing.T) {
	contract := newTestContract(1000)
	contract.Code = []byte{byte(PUSH1), 0x60, byte(PUSH1), 0x40, byte(ADD), byte(STOP)}

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
}

func TestContractAddress(t *testing.T) {
	contract := newTestContract(1000)

	if contract.Address() != testObjectAddr {
		t.Errorf("Address() = %v, want %v", contract.Address(), testObjectAddr)
	}
}

func TestContractCaller(t *testing.T) {
	contract := newTestContract(1000)

	if contract.Caller() != testCallerAddr {
		t.Errorf("Caller() = %v, want %v", contract.Caller(), testCallerAddr)
	}
}

func TestContractValue(t *testing.T) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)
	value := uint256.NewInt(12345678)
	contract := NewContract(caller, object, value, 1000, false)

	if contract.Value().Cmp(value) != 0 {
		t.Errorf("Value() = %v, want %v", contract.Value(), value)
	}
}

func TestContractSetCallCode(t *testing.T) {
	contract := newTestContract(1000)

	codeAddr := testDelegateAddr
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	code := []byte{byte(PUSH1), 0x00, byte(STOP)}

	contract.SetCallCode(&codeAddr, codeHash, code)

	if *contract.CodeAddr != codeAddr {
		t.Errorf("CodeAddr = %v, want %v", *contract.CodeAddr, codeAddr)
	}
	if contract.CodeHash != codeHash {
		t.Errorf("CodeHash = %v, want %v", contract.CodeHash, codeHash)
	}
	if string(contract.Code) != string(code) {
		t.Errorf("Code = %x, want %x", contract.Code, code)
	}
}

func TestContractAsDelegate(t *testing.T) {
	caller := AccountRef(testCallerAddr)
	parentValue := uint256.NewInt(100)
	parentContract := NewContract(caller, AccountRef(testObjectAddr), parentValue, 1000, false)
	parentContract.CallerAddress = testCallerAddr

	delegateContract := NewContract(parentContract, AccountRef(testDelegateAddr), uint256.NewInt(0), 500, false)
	delegateContract.AsDelegate()

	if delegateContract.CallerAddress != testCallerAddr {
		t.Errorf("after AsDelegate, CallerAddress = %v, want %v", delegateContract.CallerAddress, testCallerAddr)
	}
	if delegateContract.Value().Cmp(parentValue) != 0 {
		t.Errorf("after AsDelegate, Value = %v, want %v", delegateContract.Value(), parentValue)
	}
}

func TestContractJumpdests(t *testing.T) {
	contract := newTestContract(1000)

	if contract.jumpdests == nil {
		t.Error("jumpdests should be initialized")
	}
}

func TestContractJumpdestsInheritance(t *testing.T) {
	caller := AccountRef(testCallerAddr)
	parentContract := NewContract(caller, AccountRef(testObjectAddr), uint256.NewInt(0), 1000, false)
	parentContract.jumpdests[types.HexToHash("0xdead")] = []uint64{1, 2, 3}

	childContract := NewContract(parentContract, AccountRef(testDelegateAddr), uint256.NewInt(0), 500, false)

	testHash := types.HexToHash("0xtest")
	parentContract.jumpdests[testHash] = []uint64{10, 20, 30}
	if childContract.jumpdests[testHash] == nil {
		t.Error("child contract should share parent's jumpdests map")
	}
}

func TestContractSkipAnalysis(t *testing.T) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)

	contract := NewContract(caller, object, uint256.NewInt(0), 1000, true)
	if !contract.skipAnalysis {
		t.Error("skipAnalysis should be true")
	}

	contract2 := NewContract(caller, object, uint256.NewInt(0), 1000, false)
	if contract2.skipAnalysis {
		t.Error("skipAnalysis should be false")
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkNewContract(b *testing.B) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)
	value := uint256.NewInt(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NewContract(caller, object, value, 21000, false)
	}
}

func BenchmarkContractUseGas(b *testing.B) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(caller, object, uint256.NewInt(0), 1000000, false)
		for j := 0; j < 100; j++ {
			contract.UseGas(1)
		}
	}
}

func BenchmarkContractGetOp(b *testing.B) {
	contract := newTestContract(1000)
	contract.Code = make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract.GetOp(uint64(i % 1024))
	}
}

func BenchmarkContractSetCallCode(b *testing.B) {
	caller := AccountRef(testCallerAddr)
	object := AccountRef(testObjectAddr)
	codeAddr := testDelegateAddr
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	code := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(caller, object, uint256.NewInt(0), 1000, false)
		contract.SetCallCode(&codeAddr, codeHash, code)
	}
}
