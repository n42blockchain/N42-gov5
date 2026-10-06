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
	"math/big"
	"testing"
)

// push32 encodes a PUSH32 of the given 32-byte big-endian value.
func push32(v *big.Int) []byte {
	b := make([]byte, 32)
	v.FillBytes(b)
	return append([]byte{byte(PUSH32)}, b...)
}

// binOpReturnProgram builds bytecode that pushes b then a (so the opcode
// sees [a, b] with a on top per EVM push order: push a last), applies op,
// stores the 32-byte result at memory offset 0 and returns it.
func binOpReturnProgram(op OpCode, a, b *big.Int) []byte {
	var prog []byte
	prog = append(prog, push32(b)...)
	prog = append(prog, push32(a)...)
	prog = append(prog, byte(op))
	prog = append(prog, byte(PUSH1), 0x00)
	prog = append(prog, byte(MSTORE))
	prog = append(prog, byte(PUSH1), 0x20)
	prog = append(prog, byte(PUSH1), 0x00)
	prog = append(prog, byte(RETURN))
	return prog
}

func runBinOp(t *testing.T, op OpCode, a, b *big.Int) []byte {
	t.Helper()
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := binOpReturnProgram(op, a, b)
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return ret
}

func maxU256() *big.Int {
	m := new(big.Int).Lsh(big.NewInt(1), 256)
	return m.Sub(m, big.NewInt(1))
}

func TestOpcodeArithmeticExec(t *testing.T) {
	max := maxU256()
	tests := []struct {
		name string
		op   OpCode
		a, b *big.Int
		want *big.Int
	}{
		{"add_zero", ADD, big.NewInt(0), big.NewInt(0), big.NewInt(0)},
		{"add_overflow_wraps", ADD, max, big.NewInt(1), big.NewInt(0)},
		{"mul_max_by_zero", MUL, max, big.NewInt(0), big.NewInt(0)},
		{"sub_underflow_wraps", SUB, big.NewInt(0), big.NewInt(1), max},
		{"div_by_zero_is_zero", DIV, big.NewInt(10), big.NewInt(0), big.NewInt(0)},
		{"div_basic", DIV, big.NewInt(10), big.NewInt(3), big.NewInt(3)},
		{"mod_by_zero_is_zero", MOD, big.NewInt(10), big.NewInt(0), big.NewInt(0)},
		{"mod_basic", MOD, big.NewInt(10), big.NewInt(3), big.NewInt(1)},
		{"exp_zero_pow_zero_is_one", EXP, big.NewInt(0), big.NewInt(0), big.NewInt(1)},
		{"lt_true", LT, big.NewInt(1), big.NewInt(2), big.NewInt(1)},
		{"lt_false", LT, big.NewInt(2), big.NewInt(1), big.NewInt(0)},
		{"gt_true", GT, big.NewInt(2), big.NewInt(1), big.NewInt(1)},
		{"eq_true", EQ, big.NewInt(5), big.NewInt(5), big.NewInt(1)},
		{"iszero_direct_not_binop", AND, max, max, max},
		{"and_basic", AND, big.NewInt(0xFF), big.NewInt(0x0F), big.NewInt(0x0F)},
		{"or_basic", OR, big.NewInt(0xF0), big.NewInt(0x0F), big.NewInt(0xFF)},
		{"xor_basic", XOR, max, max, big.NewInt(0)},
		{"byte_extract", BYTE, big.NewInt(31), big.NewInt(0xAB), big.NewInt(0xAB)},
		{"shl_basic", SHL, big.NewInt(1), big.NewInt(1), big.NewInt(2)},
		{"shr_basic", SHR, big.NewInt(1), big.NewInt(2), big.NewInt(1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runBinOp(t, tt.op, tt.a, tt.b)
			wantBytes := make([]byte, 32)
			tt.want.FillBytes(wantBytes)
			if !bytes.Equal(got, wantBytes) {
				t.Errorf("%s(%v,%v) = %x, want %x", tt.name, tt.a, tt.b, got, wantBytes)
			}
		})
	}
}

// ternaryOpReturnProgram pushes c, b, a (top-of-stack = a) then applies op.
func ternaryOpReturnProgram(op OpCode, a, b, c *big.Int) []byte {
	var prog []byte
	prog = append(prog, push32(c)...)
	prog = append(prog, push32(b)...)
	prog = append(prog, push32(a)...)
	prog = append(prog, byte(op))
	prog = append(prog, byte(PUSH1), 0x00)
	prog = append(prog, byte(MSTORE))
	prog = append(prog, byte(PUSH1), 0x20)
	prog = append(prog, byte(PUSH1), 0x00)
	prog = append(prog, byte(RETURN))
	return prog
}

func TestOpcodeTernaryExec(t *testing.T) {
	tests := []struct {
		name       string
		op         OpCode
		a, b, c    *big.Int
		want       *big.Int
	}{
		{"addmod_basic", ADDMOD, big.NewInt(10), big.NewInt(10), big.NewInt(8), big.NewInt(4)},
		{"addmod_mod_zero", ADDMOD, big.NewInt(10), big.NewInt(10), big.NewInt(0), big.NewInt(0)},
		{"mulmod_basic", MULMOD, big.NewInt(10), big.NewInt(10), big.NewInt(8), big.NewInt(4)},
		{"mulmod_mod_zero", MULMOD, big.NewInt(10), big.NewInt(10), big.NewInt(0), big.NewInt(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
			code := ternaryOpReturnProgram(tt.op, tt.a, tt.b, tt.c)
			ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
			if err != nil {
				t.Fatalf("call failed: %v", err)
			}
			wantBytes := make([]byte, 32)
			tt.want.FillBytes(wantBytes)
			if !bytes.Equal(ret, wantBytes) {
				t.Errorf("%s(%v,%v,%v) = %x, want %x", tt.name, tt.a, tt.b, tt.c, ret, wantBytes)
			}
		})
	}
}

func TestOpcodeSignextendExec(t *testing.T) {
	// SIGNEXTEND(0, 0xFF) sign-extends a negative byte to all-ones.
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := binOpReturnProgram(SIGNEXTEND, big.NewInt(0), big.NewInt(0xFF))
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := maxU256()
	wantBytes := make([]byte, 32)
	want.FillBytes(wantBytes)
	if !bytes.Equal(ret, wantBytes) {
		t.Errorf("SIGNEXTEND(0,0xFF) = %x, want %x", ret, wantBytes)
	}
}

func TestOpcodeSdivSmodExec(t *testing.T) {
	// SDIV(-10, 3) = -3 (truncated toward zero).
	negTen := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(10))
	negThree := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(3))
	got := runBinOp(t, SDIV, negTen, big.NewInt(3))
	want := make([]byte, 32)
	negThree.FillBytes(want)
	if !bytes.Equal(got, want) {
		t.Errorf("SDIV(-10,3) = %x, want %x", got, want)
	}
}
