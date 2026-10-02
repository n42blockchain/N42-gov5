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
)

func runProgram(t *testing.T, code []byte) []byte {
	t.Helper()
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return ret
}

func TestOpcodeMstoreMload(t *testing.T) {
	// MSTORE 0x2a at offset 0, MLOAD it back, RETURN.
	code := []byte{
		byte(PUSH1), 0x2a,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x00,
		byte(MLOAD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 0x2a
	if !bytes.Equal(got, want) {
		t.Errorf("MLOAD/MSTORE roundtrip = %x, want %x", got, want)
	}
}

func TestOpcodeMstore8(t *testing.T) {
	// MSTORE8 0xAB at offset 0; the rest of the word stays zero.
	code := []byte{
		byte(PUSH1), 0xAB,
		byte(PUSH1), 0x00,
		byte(MSTORE8),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[0] = 0xAB
	if !bytes.Equal(got, want) {
		t.Errorf("MSTORE8 = %x, want %x", got, want)
	}
}

func TestOpcodeMcopy(t *testing.T) {
	// Store 0x11..0x14 pattern at offset 0, MCOPY(dst=32,src=0,len=4), return [32:64).
	code := []byte{
		byte(PUSH4), 0x11, 0x22, 0x33, 0x44,
		byte(PUSH1), 0x00, // MSTORE right-aligns the 4-byte value at [28:32)
		byte(MSTORE),
		// MCOPY(destOffset=32, offset=28, size=4)
		byte(PUSH1), 0x04,
		byte(PUSH1), 0x1c,
		byte(PUSH1), 0x20,
		byte(MCOPY),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x20,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[0], want[1], want[2], want[3] = 0x11, 0x22, 0x33, 0x44
	if !bytes.Equal(got, want) {
		t.Errorf("MCOPY = %x, want %x", got, want)
	}
}

func TestOpcodeMsize(t *testing.T) {
	// Expand memory via MSTORE at offset 0, then MSIZE should report 32.
	code := []byte{
		byte(PUSH1), 0x00,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(MSIZE),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 32
	if !bytes.Equal(got, want) {
		t.Errorf("MSIZE = %x, want %x", got, want)
	}
}

func TestOpcodeKeccak256(t *testing.T) {
	// SHA3 of the empty input is the well-known Keccak256("").
	code := []byte{
		byte(PUSH1), 0x00, // size
		byte(PUSH1), 0x00, // offset
		byte(KECCAK256),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := []byte{
		0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c, 0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
		0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b, 0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("SHA3(empty) = %x, want %x", got, want)
	}
}

func TestOpcodePush0(t *testing.T) {
	code := []byte{
		byte(PUSH0),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	if !bytes.Equal(got, want) {
		t.Errorf("PUSH0 = %x, want %x", got, want)
	}
}

func TestOpcodeDupSwap(t *testing.T) {
	// PUSH1 1, PUSH1 2, SWAP1 -> [1, 2] (top=2... after swap top=1,below=2)
	// then DUP2 duplicates the second item, verify via arithmetic.
	// Program: push 5, push 7, SWAP1 -> top=5,below=7; ADD -> 12.
	code := []byte{
		byte(PUSH1), 0x05,
		byte(PUSH1), 0x07,
		byte(SWAP1),
		byte(ADD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 12
	if !bytes.Equal(got, want) {
		t.Errorf("SWAP1+ADD = %x, want %x", got, want)
	}

	// DUP1 duplicates top: push 9, DUP1, ADD -> 18.
	code2 := []byte{
		byte(PUSH1), 0x09,
		byte(DUP1),
		byte(ADD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got2 := runProgram(t, code2)
	want2 := make([]byte, 32)
	want2[31] = 18
	if !bytes.Equal(got2, want2) {
		t.Errorf("DUP1+ADD = %x, want %x", got2, want2)
	}
}

func TestOpcodePop(t *testing.T) {
	// Push two values, POP one, return the remaining one.
	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x02,
		byte(POP),
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
		t.Errorf("POP leftover = %x, want %x", got, want)
	}
}
