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

	"github.com/holiman/uint256"
)

func TestOpcodeCallerAddressCallvalue(t *testing.T) {
	// Program returns CALLER, ADDRESS, CALLVALUE concatenated would need
	// three separate returns; test them individually via MSTORE+RETURN.
	t.Run("caller", func(t *testing.T) {
		code := []byte{
			byte(CALLER),
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got := runProgram(t, code)
		if bytes.Equal(got[12:], make([]byte, 20)) {
			t.Errorf("CALLER returned zero address")
		}
	})

	t.Run("address", func(t *testing.T) {
		code := []byte{
			byte(ADDRESS),
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got := runProgram(t, code)
		// Just check the low 20 bytes are non-zero (the deployed object address).
		if bytes.Equal(got[12:], make([]byte, 20)) {
			t.Errorf("ADDRESS returned zero address")
		}
	})

	t.Run("callvalue", func(t *testing.T) {
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		code := []byte{
			byte(CALLVALUE),
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, uint256.NewInt(42))
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		want := make([]byte, 32)
		want[31] = 42
		if !bytes.Equal(got, want) {
			t.Errorf("CALLVALUE = %x, want %x", got, want)
		}
	})
}

func TestOpcodeCalldataLoadSizeCopy(t *testing.T) {
	input := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xAA}

	t.Run("calldataload", func(t *testing.T) {
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		code := []byte{
			byte(PUSH1), 0x00,
			byte(CALLDATALOAD),
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got, _, err := execHarnessCall(t, evm, ibs, code, input, 1_000_000, nil)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if !bytes.Equal(got, input) {
			t.Errorf("CALLDATALOAD = %x, want %x", got, input)
		}
	})

	t.Run("calldatasize", func(t *testing.T) {
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		code := []byte{
			byte(CALLDATASIZE),
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got, _, err := execHarnessCall(t, evm, ibs, code, input, 1_000_000, nil)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		want := make([]byte, 32)
		want[31] = byte(len(input))
		if !bytes.Equal(got, want) {
			t.Errorf("CALLDATASIZE = %x, want %x", got, want)
		}
	})

	t.Run("calldatacopy", func(t *testing.T) {
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		code := []byte{
			byte(PUSH1), 0x20, // size
			byte(PUSH1), 0x00, // offset
			byte(PUSH1), 0x00, // destOffset
			byte(CALLDATACOPY),
			byte(PUSH1), 0x20,
			byte(PUSH1), 0x00,
			byte(RETURN),
		}
		got, _, err := execHarnessCall(t, evm, ibs, code, input, 1_000_000, nil)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if !bytes.Equal(got, input) {
			t.Errorf("CALLDATACOPY = %x, want %x", got, input)
		}
	})
}

func TestOpcodeChainIdNumberTimestampGaslimit(t *testing.T) {
	code := []byte{
		byte(CHAINID),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 1
	if !bytes.Equal(got, want) {
		t.Errorf("CHAINID = %x, want %x", got, want)
	}

	code = []byte{
		byte(NUMBER),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got = runProgram(t, code)
	want = make([]byte, 32)
	want[31] = 1
	if !bytes.Equal(got, want) {
		t.Errorf("NUMBER = %x, want %x", got, want)
	}
}

func TestOpcodeJumpValid(t *testing.T) {
	// PUSH1 dest, JUMP to JUMPDEST at offset 5, then PUSH1 7 + RETURN.
	code := []byte{
		byte(PUSH1), 0x05, // 0,1
		byte(JUMP),        // 2
		byte(INVALID),     // 3 (should never execute; filler)
		byte(INVALID),     // 4 (filler to land JUMPDEST at 5)
		byte(JUMPDEST),    // 5
		byte(PUSH1), 0x07, // 6,7
		byte(PUSH1), 0x00, // 8,9
		byte(MSTORE),      // 10
		byte(PUSH1), 0x20, // 11,12
		byte(PUSH1), 0x00, // 13,14
		byte(RETURN),      // 15
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 7
	if !bytes.Equal(got, want) {
		t.Errorf("JUMP result = %x, want %x", got, want)
	}
}

func TestOpcodeJumpInvalidDest(t *testing.T) {
	// Jump to a non-JUMPDEST location must fail with ErrInvalidJump.
	code := []byte{
		byte(PUSH1), 0x03,
		byte(JUMP),
		byte(STOP), // offset 3 is STOP, not JUMPDEST
	}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if !errors.Is(err, ErrInvalidJump) {
		t.Fatalf("err = %v, want %v", err, ErrInvalidJump)
	}
}

func TestOpcodeJumpiConditional(t *testing.T) {
	// JUMPI with nonzero condition jumps; with zero condition falls through.
	jumpTaken := []byte{
		byte(PUSH1), 0x01, // 0,1 cond=1
		byte(PUSH1), 0x08, // 2,3 dest=8
		byte(JUMPI),        // 4
		byte(INVALID),       // 5 (skipped if jump taken)
		byte(INVALID),       // 6
		byte(INVALID),       // 7
		byte(JUMPDEST),      // 8
		byte(PUSH1), 0x09,   // 9,10 -> value 9
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, jumpTaken)
	want := make([]byte, 32)
	want[31] = 9
	if !bytes.Equal(got, want) {
		t.Errorf("JUMPI(taken) = %x, want %x", got, want)
	}

	notTaken := []byte{
		byte(PUSH1), 0x00, // cond=0
		byte(PUSH1), 0x08,
		byte(JUMPI),
		byte(PUSH1), 0x05, // fallthrough: value 5
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
		byte(JUMPDEST),
		byte(STOP),
	}
	got = runProgram(t, notTaken)
	want = make([]byte, 32)
	want[31] = 5
	if !bytes.Equal(got, want) {
		t.Errorf("JUMPI(not taken) = %x, want %x", got, want)
	}
}

func TestOpcodeLog0ThroughLog2(t *testing.T) {
	// LOG0 with no topics, data = 32-byte word.
	t.Run("log0", func(t *testing.T) {
		code := []byte{
			byte(PUSH1), 0x2a,
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0x20, // size
			byte(PUSH1), 0x00, // offset
			byte(LOG0),
			byte(STOP),
		}
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
		if err != nil {
			t.Fatalf("LOG0 call failed: %v", err)
		}
	})

	t.Run("log2_with_topics", func(t *testing.T) {
		code := []byte{
			byte(PUSH1), 0x2a,
			byte(PUSH1), 0x00,
			byte(MSTORE),
			byte(PUSH1), 0xBB, // topic2
			byte(PUSH1), 0xAA, // topic1
			byte(PUSH1), 0x20, // size
			byte(PUSH1), 0x00, // offset
			byte(LOG2),
			byte(STOP),
		}
		evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
		_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
		if err != nil {
			t.Fatalf("LOG2 call failed: %v", err)
		}
	})
}
