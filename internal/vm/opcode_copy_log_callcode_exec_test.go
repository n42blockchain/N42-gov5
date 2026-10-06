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

// TestVMTOpCodeCopyBoundary verifies CODECOPY zero-pads when the requested
// range runs past the end of the contract's own code.
func TestVMTOpCodeCopyBoundary(t *testing.T) {
	// Code: CODECOPY(destOffset=0, codeOffset=0, length=64) then RETURN(0,64).
	// The contract code itself is shorter than 64 bytes, so the tail must be
	// zero-padded rather than erroring.
	code := []byte{
		byte(PUSH1), 0x40, // length = 64
		byte(PUSH1), 0x00, // codeOffset = 0
		byte(PUSH1), 0x00, // destOffset = 0
		byte(CODECOPY),
		byte(PUSH1), 0x40,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if len(ret) != 64 {
		t.Fatalf("expected 64 bytes, got %d", len(ret))
	}
	// Bytes beyond len(code) must be zero.
	for i := len(code); i < 64; i++ {
		if ret[i] != 0 {
			t.Fatalf("expected zero padding at byte %d, got %x", i, ret[i])
		}
	}
}

// TestVMTOpExtCodeCopyBoundary verifies EXTCODECOPY zero-pads past the end
// of the target account's code.
func TestVMTOpExtCodeCopyBoundary(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	target := types.HexToAddress("0x00000000000000000000000000000000beef42")
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, []byte{0x01, 0x02, 0x03})

	addrWord := make([]byte, 32)
	copy(addrWord[12:], target.Bytes())

	// EXTCODECOPY pops address, destOffset, codeOffset, length (in that
	// order), so the address must be pushed last (topmost).
	code := []byte{
		byte(PUSH1), 0x20, // length = 32
		byte(PUSH1), 0x00, // codeOffset = 0
		byte(PUSH1), 0x00, // destOffset = 0
		byte(PUSH32),
	}
	code = append(code, addrWord...)
	code = append(code,
		byte(EXTCODECOPY),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if len(ret) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(ret))
	}
	if ret[0] != 0x01 || ret[1] != 0x02 || ret[2] != 0x03 {
		t.Fatalf("expected code prefix preserved, got %x", ret[:3])
	}
	for i := 3; i < 32; i++ {
		if ret[i] != 0 {
			t.Fatalf("expected zero padding at byte %d, got %x", i, ret[i])
		}
	}
}

// TestVMTOpReturnDataCopyOutOfBounds verifies RETURNDATACOPY reverts the
// whole call when the requested range exceeds the actual return data size.
func TestVMTOpReturnDataCopyOutOfBounds(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())

	// Sub-callee returns exactly 4 bytes.
	callee := types.HexToAddress("0x00000000000000000000000000000000ca11ee")
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{
		byte(PUSH4), 0xde, 0xad, 0xbe, 0xef,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x04,
		byte(PUSH1), 0x1c, // offset 28 so the 4 bytes land at the end of the word
		byte(RETURN),
	})

	calleeWord := make([]byte, 32)
	copy(calleeWord[12:], callee.Bytes())

	// Caller: STATICCALL(gas, callee, 0,0,0,0) then RETURNDATACOPY(0, 0, 32)
	// which must fail because only 4 bytes of return data exist.
	code := []byte{
		byte(PUSH1), 0x00, // retSize
		byte(PUSH1), 0x00, // retOffset
		byte(PUSH1), 0x00, // argsSize
		byte(PUSH1), 0x00, // argsOffset
		byte(PUSH32),
	}
	code = append(code, calleeWord...)
	code = append(code,
		byte(PUSH2), 0x9c, 0x40, // gas
		byte(STATICCALL),
		byte(POP), // drop success flag

		byte(PUSH1), 0x20, // length = 32 (more than the 4 returned)
		byte(PUSH1), 0x00, // dataOffset
		byte(PUSH1), 0x00, // memOffset
		byte(RETURNDATACOPY),
		byte(STOP),
	)

	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != ErrReturnDataOutOfBounds {
		t.Fatalf("expected ErrReturnDataOutOfBounds, got %v", err)
	}
}

// TestVMTOpLog1 covers the LOG1 opcode (one indexed topic).
func TestVMTOpLog1(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(PUSH1), 0x2a, // data word = 42
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x11, // topic1
		byte(PUSH1), 0x20, // size
		byte(PUSH1), 0x00, // offset
		byte(LOG1),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	logs := ibs.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if len(logs[0].Topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(logs[0].Topics))
	}
}

// TestVMTOpLog3 covers the LOG3 opcode (three indexed topics).
func TestVMTOpLog3(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(PUSH1), 0x2a,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x03, // topic3
		byte(PUSH1), 0x02, // topic2
		byte(PUSH1), 0x01, // topic1
		byte(PUSH1), 0x20, // size
		byte(PUSH1), 0x00, // offset
		byte(LOG3),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	logs := ibs.Logs()
	if len(logs) != 1 || len(logs[0].Topics) != 3 {
		t.Fatalf("expected 1 log with 3 topics, got %+v", logs)
	}
}

// TestVMTOpLog4 covers the LOG4 opcode (four indexed topics).
func TestVMTOpLog4(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(PUSH1), 0x2a,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x04,
		byte(PUSH1), 0x03,
		byte(PUSH1), 0x02,
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(LOG4),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	logs := ibs.Logs()
	if len(logs) != 1 || len(logs[0].Topics) != 4 {
		t.Fatalf("expected 1 log with 4 topics, got %+v", logs)
	}
}

// TestVMTOpCallCode exercises CALLCODE: the callee's code runs with the
// caller's storage/context (SSTORE in the callee must land in the caller's
// storage, not the callee's).
func TestVMTOpCallCode(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())

	callee := types.HexToAddress("0x00000000000000000000000000000000c0deee")
	ibs.CreateAccount(callee, true)
	// Callee: SSTORE(slot 0x5, value 0x99), then STOP.
	ibs.SetCode(callee, []byte{
		byte(PUSH1), 0x99,
		byte(PUSH1), 0x05,
		byte(SSTORE),
		byte(STOP),
	})

	calleeWord := make([]byte, 32)
	copy(calleeWord[12:], callee.Bytes())

	code := []byte{
		byte(PUSH1), 0x00, // retSize
		byte(PUSH1), 0x00, // retOffset
		byte(PUSH1), 0x00, // argsSize
		byte(PUSH1), 0x00, // argsOffset
		byte(PUSH1), 0x00, // value
		byte(PUSH32),
	}
	code = append(code, calleeWord...)
	code = append(code,
		byte(PUSH2), 0x9c, 0x40, // gas
		byte(CALLCODE),
		byte(POP),
		byte(STOP),
	)

	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}

	object := types.HexToAddress("0x0bec70beef0bec70beef0bec70beef0bec70bee")
	key := types.BytesToHash([]byte{0x05})
	var got uint256.Int
	ibs.GetState(object, &key, &got)
	if got.Cmp(uint256.NewInt(0x99)) != 0 {
		t.Fatalf("CALLCODE must write to the caller's (object) storage: got %s want 0x99", got.String())
	}

	// The callee itself must NOT have the slot set.
	var calleeVal uint256.Int
	ibs.GetState(callee, &key, &calleeVal)
	if !calleeVal.IsZero() {
		t.Fatalf("CALLCODE must not write to the callee's own storage, got %s", calleeVal.String())
	}
}
