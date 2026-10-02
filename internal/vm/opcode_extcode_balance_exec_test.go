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

func TestOpcodeBalance(t *testing.T) {
	target := types.HexToAddress("0xba1a0000000000000000000000000000ba1a00")
	code := []byte{byte(PUSH20)}
	code = append(code, target.Bytes()...)
	code = append(code, byte(BALANCE), byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(target, true)
	ibs.AddBalance(target, uint256.NewInt(7))

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := make([]byte, 32)
	want[31] = 7
	if !bytes.Equal(got, want) {
		t.Errorf("BALANCE = %x, want %x", got, want)
	}
}

func TestOpcodeBalanceNonexistentAccountIsZero(t *testing.T) {
	target := types.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	code := []byte{byte(PUSH20)}
	code = append(code, target.Bytes()...)
	code = append(code, byte(BALANCE), byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	got := runProgram(t, code)
	want := make([]byte, 32)
	if !bytes.Equal(got, want) {
		t.Errorf("BALANCE(nonexistent) = %x, want 0", got)
	}
}

func TestOpcodeExtcodesize(t *testing.T) {
	target := types.HexToAddress("0xc0de00000000000000000000000000000c0de0")
	targetCode := []byte{byte(STOP), byte(STOP), byte(STOP)}
	code := []byte{byte(PUSH20)}
	code = append(code, target.Bytes()...)
	code = append(code, byte(EXTCODESIZE), byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, targetCode)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := make([]byte, 32)
	want[31] = byte(len(targetCode))
	if !bytes.Equal(got, want) {
		t.Errorf("EXTCODESIZE = %x, want %x", got, want)
	}
}

func TestOpcodeExtcodehash(t *testing.T) {
	target := types.HexToAddress("0xc0de00000000000000000000000000000c0de0")
	targetCode := []byte{byte(STOP)}
	code := []byte{byte(PUSH20)}
	code = append(code, target.Bytes()...)
	code = append(code, byte(EXTCODEHASH), byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, targetCode)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if bytes.Equal(got, make([]byte, 32)) {
		t.Errorf("EXTCODEHASH of a contract with code returned zero hash")
	}
}

func TestOpcodeExtcodehashNonexistentIsZero(t *testing.T) {
	target := types.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	code := []byte{byte(PUSH20)}
	code = append(code, target.Bytes()...)
	code = append(code, byte(EXTCODEHASH), byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	got := runProgram(t, code)
	want := make([]byte, 32)
	if !bytes.Equal(got, want) {
		t.Errorf("EXTCODEHASH(nonexistent) = %x, want 0", got)
	}
}

func TestOpcodeExtcodecopy(t *testing.T) {
	target := types.HexToAddress("0xc0de00000000000000000000000000000c0de0")
	targetCode := []byte{0x11, 0x22, 0x33, 0x44}
	code := []byte{
		byte(PUSH1), 0x04, // size
		byte(PUSH1), 0x00, // codeOffset
		byte(PUSH1), 0x00, // destOffset
		byte(PUSH20),
	}
	code = append(code, target.Bytes()...)
	code = append(code, byte(EXTCODECOPY))
	code = append(code, byte(PUSH1), 0x04, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, targetCode)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if !bytes.Equal(got, targetCode) {
		t.Errorf("EXTCODECOPY = %x, want %x", got, targetCode)
	}
}
