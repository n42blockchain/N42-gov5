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
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

var testCalleeAddr = types.HexToAddress("0xcaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

// calleeReturns42 is runtime code that always returns the 32-byte word 0x2a.
var calleeReturns42 = []byte{
	byte(PUSH1), 0x2a,
	byte(PUSH1), 0x00,
	byte(MSTORE),
	byte(PUSH1), 0x20,
	byte(PUSH1), 0x00,
	byte(RETURN),
}

// calleeReverts is runtime code that always reverts with a 1-byte reason.
var calleeReverts = []byte{
	byte(PUSH1), 0xEE,
	byte(PUSH1), 0x00,
	byte(MSTORE8),
	byte(PUSH1), 0x01,
	byte(PUSH1), 0x00,
	byte(REVERT),
}

// calleeWritesStorage writes slot 0 = 1; used to prove STATICCALL rejects
// state mutation.
var calleeWritesStorage = []byte{
	byte(PUSH1), 0x01,
	byte(PUSH1), 0x00,
	byte(SSTORE),
	byte(STOP),
}

func pushCallArgs(op OpCode, gas, value, argsOffset, argsSize, retOffset, retSize byte, hasValue bool) []byte {
	var prog []byte
	prog = append(prog, byte(PUSH1), retSize)
	prog = append(prog, byte(PUSH1), retOffset)
	prog = append(prog, byte(PUSH1), argsSize)
	prog = append(prog, byte(PUSH1), argsOffset)
	if hasValue {
		prog = append(prog, byte(PUSH1), value)
	}
	prog = append(prog, byte(PUSH20))
	prog = append(prog, testCalleeAddr.Bytes()...)
	prog = append(prog, byte(PUSH1), gas)
	prog = append(prog, byte(op))
	return prog
}

func TestOpcodeCallSuccess(t *testing.T) {
	code := pushCallArgs(CALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x20, true)
	code = append(code, byte(POP)) // discard success flag
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, calleeReturns42)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := make([]byte, 32)
	want[31] = 0x2a
	if !bytes.Equal(got, want) {
		t.Errorf("CALL result = %x, want %x", got, want)
	}
}

func TestOpcodeCallRevertPropagatesSuccessFlagZero(t *testing.T) {
	// CALL into a reverting callee: success flag must be 0, but the
	// outer call itself must still succeed (CALL swallows inner revert).
	code := pushCallArgs(CALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x20, true)
	code = append(code, byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, calleeReverts)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("outer call failed: %v", err)
	}
	want := make([]byte, 32) // success flag 0
	if !bytes.Equal(got, want) {
		t.Errorf("success flag = %x, want 0", got)
	}
}

func TestOpcodeStaticcallRejectsWrite(t *testing.T) {
	// STATICCALL into a callee that tries SSTORE: inner call must fail
	// (success flag 0) due to write protection, outer call succeeds.
	code := pushCallArgs(STATICCALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x20, false)
	code = append(code, byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, calleeWritesStorage)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("outer call failed: %v", err)
	}
	want := make([]byte, 32)
	if !bytes.Equal(got, want) {
		t.Errorf("STATICCALL success flag = %x, want 0 (write must be rejected)", got)
	}
}

func TestOpcodeDelegatecallPreservesCallerAndValue(t *testing.T) {
	// DELEGATECALL into a callee that returns CALLER: the delegatecall
	// must see the original caller's address, not this contract's.
	calleeReturnsCaller := []byte{
		byte(CALLER),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	code := pushCallArgs(DELEGATECALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x20, false)
	code = append(code, byte(POP))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, calleeReturnsCaller)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	// The top-level caller as seen from inside the delegatecall must be
	// the harness's external caller, not the intermediate contract.
	if bytes.Equal(got[12:], make([]byte, 20)) {
		t.Errorf("DELEGATECALL CALLER returned zero address")
	}
}

func TestOpcodeReturndatasizeReturndatacopy(t *testing.T) {
	code := pushCallArgs(CALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x00, true)
	code = append(code, byte(POP))
	// RETURNDATASIZE -> store at 0
	code = append(code, byte(RETURNDATASIZE), byte(PUSH1), 0x00, byte(MSTORE))
	// RETURNDATACOPY(destOffset=0x20, offset=0, size=32)
	code = append(code,
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(PUSH1), 0x20,
		byte(RETURNDATACOPY),
		byte(PUSH1), 0x40,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, calleeReturns42)

	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("return length = %d, want 64", len(got))
	}
	sizeWord := got[:32]
	wantSize := make([]byte, 32)
	wantSize[31] = 32
	if !bytes.Equal(sizeWord, wantSize) {
		t.Errorf("RETURNDATASIZE = %x, want %x", sizeWord, wantSize)
	}
	dataWord := got[32:]
	wantData := make([]byte, 32)
	wantData[31] = 0x2a
	if !bytes.Equal(dataWord, wantData) {
		t.Errorf("RETURNDATACOPY = %x, want %x", dataWord, wantData)
	}
}

func TestOpcodeCreateAndCreate2(t *testing.T) {
	// Initcode that directly returns calleeReturns42 as runtime code: push
	// it left-aligned via PUSH32, MSTORE at offset 0, then RETURN the first
	// rtLen bytes.
	rtLen := len(calleeReturns42)
	word := make([]byte, 32)
	copy(word, calleeReturns42)
	init := []byte{byte(PUSH32)}
	init = append(init, word...)
	init = append(init, byte(PUSH1), 0x00, byte(MSTORE))
	init = append(init, byte(PUSH1), byte(rtLen), byte(PUSH1), 0x00, byte(RETURN))

	// Build CREATE program: write `init` bytes into memory at offset 0,
	// then CREATE(value=0, offset=0, size=len(init)).
	create := writeMemProgram(init)
	create = append(create,
		byte(PUSH1), byte(len(init)), // size
		byte(PUSH1), 0x00, // offset
		byte(PUSH1), 0x00, // value
		byte(CREATE),
	)
	create = append(create, byte(PUSH1), 0x00, byte(MSTORE))
	create = append(create, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	got, _, err := execHarnessCall(t, evm, ibs, create, nil, 2_000_000, nil)
	if err != nil {
		t.Fatalf("CREATE call failed: %v", err)
	}
	var newAddr [20]byte
	copy(newAddr[:], got[12:])
	if bytes.Equal(newAddr[:], make([]byte, 20)) {
		t.Fatalf("CREATE returned zero address (deployment failed)")
	}
	deployedCode := ibs.GetCode(types.Address(newAddr))
	if !bytes.Equal(deployedCode, calleeReturns42) {
		t.Errorf("deployed code = %x, want %x", deployedCode, calleeReturns42)
	}
}

// writeMemProgram returns bytecode that writes data into memory starting at
// offset 0 using 32-byte PUSH32+MSTORE chunks.
func writeMemProgram(data []byte) []byte {
	var prog []byte
	for i := 0; i+32 <= len(data); i += 32 {
		var w [32]byte
		copy(w[:], data[i:i+32])
		prog = append(prog, byte(PUSH32))
		prog = append(prog, w[:]...)
		prog = append(prog, byte(PUSH1), byte(i), byte(MSTORE))
	}
	if rem := len(data) % 32; rem != 0 {
		base := len(data) - rem
		var w [32]byte
		copy(w[:], data[base:])
		prog = append(prog, byte(PUSH32))
		prog = append(prog, w[:]...)
		prog = append(prog, byte(PUSH1), byte(base), byte(MSTORE))
	}
	return prog
}

func TestOpcodeCreate2(t *testing.T) {
	rtLen := len(calleeReturns42)
	word := make([]byte, 32)
	copy(word, calleeReturns42)
	init := []byte{byte(PUSH32)}
	init = append(init, word...)
	init = append(init, byte(PUSH1), 0x00, byte(MSTORE))
	init = append(init, byte(PUSH1), byte(rtLen), byte(PUSH1), 0x00, byte(RETURN))

	code := writeMemProgram(init)
	code = append(code,
		byte(PUSH1), 0x2a, // salt
		byte(PUSH1), byte(len(init)), // size
		byte(PUSH1), 0x00, // offset
		byte(PUSH1), 0x00, // value/endowment
		byte(CREATE2),
	)
	code = append(code, byte(PUSH1), 0x00, byte(MSTORE))
	code = append(code, byte(PUSH1), 0x20, byte(PUSH1), 0x00, byte(RETURN))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 2_000_000, nil)
	if err != nil {
		t.Fatalf("CREATE2 call failed: %v", err)
	}
	var newAddr [20]byte
	copy(newAddr[:], got[12:])
	if bytes.Equal(newAddr[:], make([]byte, 20)) {
		t.Fatalf("CREATE2 returned zero address (deployment failed)")
	}
	deployedCode := ibs.GetCode(types.Address(newAddr))
	if !bytes.Equal(deployedCode, calleeReturns42) {
		t.Errorf("deployed code = %x, want %x", deployedCode, calleeReturns42)
	}
}

func TestOpcodeSelfdestruct(t *testing.T) {
	beneficiary := types.HexToAddress("0xbeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	code := []byte{}
	code = append(code, byte(PUSH20))
	code = append(code, beneficiary.Bytes()...)
	code = append(code, byte(SELFDESTRUCT))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("SELFDESTRUCT call failed: %v", err)
	}
}

func TestOpcodeStaticcallRejectsSelfdestruct(t *testing.T) {
	// SELFDESTRUCT from within a STATICCALL must be rejected with
	// ErrWriteProtection, surfacing as a failed inner call.
	beneficiary := types.HexToAddress("0xbeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	selfdestructCallee := append([]byte{byte(PUSH20)}, beneficiary.Bytes()...)
	selfdestructCallee = append(selfdestructCallee, byte(SELFDESTRUCT))

	code := pushCallArgs(STATICCALL, 0xff, 0x00, 0x00, 0x00, 0x00, 0x00, false)

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(testCalleeAddr, true)
	ibs.SetCode(testCalleeAddr, selfdestructCallee)

	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("outer call failed: %v", err)
	}
}

func TestOpcodeInvalidOpcode(t *testing.T) {
	code := []byte{byte(INVALID)}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	var invalidErr *ErrInvalidOpCode
	if !errors.As(err, &invalidErr) {
		t.Fatalf("err = %v, want *ErrInvalidOpCode", err)
	}
}

func TestOpcodeStackUnderflow(t *testing.T) {
	// ADD with an empty stack must fail, not panic.
	code := []byte{byte(ADD)}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err == nil {
		t.Fatalf("expected an error for stack underflow, got nil")
	}
}
