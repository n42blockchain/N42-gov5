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
)

// =============================================================================
// OpCode Tests (Reference: go-ethereum/core/vm/opcodes_test.go)
// =============================================================================

func TestOpCodeString(t *testing.T) {
	tests := []struct {
		op       OpCode
		expected string
	}{
		{STOP, "STOP"},
		{ADD, "ADD"},
		{MUL, "MUL"},
		{SUB, "SUB"},
		{DIV, "DIV"},
		{SDIV, "SDIV"},
		{MOD, "MOD"},
		{SMOD, "SMOD"},
		{ADDMOD, "ADDMOD"},
		{MULMOD, "MULMOD"},
		{EXP, "EXP"},
		{SIGNEXTEND, "SIGNEXTEND"},
		{LT, "LT"},
		{GT, "GT"},
		{SLT, "SLT"},
		{SGT, "SGT"},
		{EQ, "EQ"},
		{ISZERO, "ISZERO"},
		{AND, "AND"},
		{OR, "OR"},
		{XOR, "XOR"},
		{NOT, "NOT"},
		{BYTE, "BYTE"},
		{SHL, "SHL"},
		{SHR, "SHR"},
		{SAR, "SAR"},
		{KECCAK256, "KECCAK256"},
		{ADDRESS, "ADDRESS"},
		{BALANCE, "BALANCE"},
		{ORIGIN, "ORIGIN"},
		{CALLER, "CALLER"},
		{CALLVALUE, "CALLVALUE"},
		{CALLDATALOAD, "CALLDATALOAD"},
		{CALLDATASIZE, "CALLDATASIZE"},
		{CALLDATACOPY, "CALLDATACOPY"},
		{CODESIZE, "CODESIZE"},
		{CODECOPY, "CODECOPY"},
		{GASPRICE, "GASPRICE"},
		{EXTCODESIZE, "EXTCODESIZE"},
		{EXTCODECOPY, "EXTCODECOPY"},
		{RETURNDATASIZE, "RETURNDATASIZE"},
		{RETURNDATACOPY, "RETURNDATACOPY"},
		{EXTCODEHASH, "EXTCODEHASH"},
		{BLOCKHASH, "BLOCKHASH"},
		{COINBASE, "COINBASE"},
		{TIMESTAMP, "TIMESTAMP"},
		{NUMBER, "NUMBER"},
		{DIFFICULTY, "DIFFICULTY"},
		{GASLIMIT, "GASLIMIT"},
		{CHAINID, "CHAINID"},
		{SELFBALANCE, "SELFBALANCE"},
		{BASEFEE, "BASEFEE"},
		{BLOBHASH, "BLOBHASH"},
		{BLOBBASEFEE, "BLOBBASEFEE"},
		{POP, "POP"},
		{MLOAD, "MLOAD"},
		{MSTORE, "MSTORE"},
		{MSTORE8, "MSTORE8"},
		{SLOAD, "SLOAD"},
		{SSTORE, "SSTORE"},
		{JUMP, "JUMP"},
		{JUMPI, "JUMPI"},
		{PC, "PC"},
		{MSIZE, "MSIZE"},
		{GAS, "GAS"},
		{JUMPDEST, "JUMPDEST"},
		{TLOAD, "TLOAD"},
		{TSTORE, "TSTORE"},
		{MCOPY, "MCOPY"},
		{PUSH0, "PUSH0"},
		{PUSH1, "PUSH1"},
		{PUSH2, "PUSH2"},
		{PUSH32, "PUSH32"},
		{DUP1, "DUP1"},
		{DUP16, "DUP16"},
		{SWAP1, "SWAP1"},
		{SWAP16, "SWAP16"},
		{LOG0, "LOG0"},
		{LOG4, "LOG4"},
		{CREATE, "CREATE"},
		{CALL, "CALL"},
		{CALLCODE, "CALLCODE"},
		{RETURN, "RETURN"},
		{DELEGATECALL, "DELEGATECALL"},
		{CREATE2, "CREATE2"},
		{STATICCALL, "STATICCALL"},
		{REVERT, "REVERT"},
		{INVALID, "INVALID"},
		{SELFDESTRUCT, "SELFDESTRUCT"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.op.String(); got != tt.expected {
				t.Errorf("OpCode(%#x).String() = %q, want %q", byte(tt.op), got, tt.expected)
			}
		})
	}

	t.Logf("✓ All opcode strings match expected values")
}

func TestOpCodeStringUndefined(t *testing.T) {
	// Test undefined opcode
	undefinedOp := OpCode(0x21) // Not defined
	str := undefinedOp.String()
	if str == "" {
		t.Error("Undefined opcode should have non-empty string")
	}
	t.Logf("Undefined opcode string: %s", str)

	t.Logf("✓ Undefined opcodes return informative strings")
}

func TestStringToOp(t *testing.T) {
	tests := []struct {
		name     string
		expected OpCode
	}{
		{"STOP", STOP},
		{"ADD", ADD},
		{"MUL", MUL},
		{"SUB", SUB},
		{"DIV", DIV},
		{"KECCAK256", KECCAK256},
		{"PUSH1", PUSH1},
		{"PUSH32", PUSH32},
		{"DUP1", DUP1},
		{"SWAP1", SWAP1},
		{"CALL", CALL},
		{"RETURN", RETURN},
		{"REVERT", REVERT},
		{"SELFDESTRUCT", SELFDESTRUCT},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StringToOp(tt.name); got != tt.expected {
				t.Errorf("StringToOp(%q) = %#x, want %#x", tt.name, byte(got), byte(tt.expected))
			}
		})
	}

	t.Logf("✓ StringToOp converts strings to opcodes correctly")
}

func TestStringToOpUnknown(t *testing.T) {
	// Unknown string should return 0 (STOP)
	result := StringToOp("UNKNOWN_OPCODE")
	if result != STOP {
		t.Errorf("StringToOp for unknown string = %#x, want %#x (STOP)", byte(result), byte(STOP))
	}

	t.Logf("✓ StringToOp returns STOP for unknown strings")
}

func TestOpCodeIsPush(t *testing.T) {
	pushOps := []OpCode{
		PUSH1, PUSH2, PUSH3, PUSH4, PUSH5, PUSH6, PUSH7, PUSH8,
		PUSH9, PUSH10, PUSH11, PUSH12, PUSH13, PUSH14, PUSH15, PUSH16,
		PUSH17, PUSH18, PUSH19, PUSH20, PUSH21, PUSH22, PUSH23, PUSH24,
		PUSH25, PUSH26, PUSH27, PUSH28, PUSH29, PUSH30, PUSH31, PUSH32,
	}

	for _, op := range pushOps {
		if !op.IsPush() {
			t.Errorf("%s.IsPush() = false, want true", op)
		}
	}

	nonPushOps := []OpCode{STOP, ADD, MUL, CALL, RETURN, PUSH0}
	for _, op := range nonPushOps {
		if op.IsPush() {
			t.Errorf("%s.IsPush() = true, want false", op)
		}
	}

	t.Logf("✓ IsPush correctly identifies PUSH opcodes")
}

func TestOpCodeIsStaticJump(t *testing.T) {
	if !JUMP.IsStaticJump() {
		t.Error("JUMP.IsStaticJump() = false, want true")
	}

	nonJumpOps := []OpCode{STOP, ADD, JUMPI, CALL, RETURN}
	for _, op := range nonJumpOps {
		if op.IsStaticJump() {
			t.Errorf("%s.IsStaticJump() = true, want false", op)
		}
	}

	t.Logf("✓ IsStaticJump correctly identifies JUMP opcode")
}

func TestOpCodeValues(t *testing.T) {
	// Test that opcode byte values match Ethereum Yellow Paper
	tests := []struct {
		op       OpCode
		expected byte
	}{
		{STOP, 0x00},
		{ADD, 0x01},
		{MUL, 0x02},
		{SUB, 0x03},
		{DIV, 0x04},
		{SDIV, 0x05},
		{MOD, 0x06},
		{SMOD, 0x07},
		{ADDMOD, 0x08},
		{MULMOD, 0x09},
		{EXP, 0x0a},
		{SIGNEXTEND, 0x0b},
		{LT, 0x10},
		{GT, 0x11},
		{SLT, 0x12},
		{SGT, 0x13},
		{EQ, 0x14},
		{ISZERO, 0x15},
		{AND, 0x16},
		{OR, 0x17},
		{XOR, 0x18},
		{NOT, 0x19},
		{BYTE, 0x1a},
		{SHL, 0x1b},
		{SHR, 0x1c},
		{SAR, 0x1d},
		{KECCAK256, 0x20},
		{ADDRESS, 0x30},
		{BALANCE, 0x31},
		{ORIGIN, 0x32},
		{CALLER, 0x33},
		{CALLVALUE, 0x34},
		{CALLDATALOAD, 0x35},
		{CALLDATASIZE, 0x36},
		{CALLDATACOPY, 0x37},
		{CODESIZE, 0x38},
		{CODECOPY, 0x39},
		{GASPRICE, 0x3a},
		{EXTCODESIZE, 0x3b},
		{EXTCODECOPY, 0x3c},
		{RETURNDATASIZE, 0x3d},
		{RETURNDATACOPY, 0x3e},
		{EXTCODEHASH, 0x3f},
		{BLOCKHASH, 0x40},
		{COINBASE, 0x41},
		{TIMESTAMP, 0x42},
		{NUMBER, 0x43},
		{DIFFICULTY, 0x44},
		{GASLIMIT, 0x45},
		{CHAINID, 0x46},
		{SELFBALANCE, 0x47},
		{BASEFEE, 0x48},
		{BLOBHASH, 0x49},
		{BLOBBASEFEE, 0x4a},
		{POP, 0x50},
		{MLOAD, 0x51},
		{MSTORE, 0x52},
		{MSTORE8, 0x53},
		{SLOAD, 0x54},
		{SSTORE, 0x55},
		{JUMP, 0x56},
		{JUMPI, 0x57},
		{PC, 0x58},
		{MSIZE, 0x59},
		{GAS, 0x5a},
		{JUMPDEST, 0x5b},
		{TLOAD, 0x5c},
		{TSTORE, 0x5d},
		{MCOPY, 0x5e},
		{PUSH0, 0x5f},
		{PUSH1, 0x60},
		{PUSH32, 0x7f},
		{DUP1, 0x80},
		{DUP16, 0x8f},
		{SWAP1, 0x90},
		{SWAP16, 0x9f},
		{LOG0, 0xa0},
		{LOG4, 0xa4},
		{CREATE, 0xf0},
		{CALL, 0xf1},
		{CALLCODE, 0xf2},
		{RETURN, 0xf3},
		{DELEGATECALL, 0xf4},
		{CREATE2, 0xf5},
		{STATICCALL, 0xfa},
		{REVERT, 0xfd},
		{INVALID, 0xfe},
		{SELFDESTRUCT, 0xff},
	}

	for _, tt := range tests {
		if byte(tt.op) != tt.expected {
			t.Errorf("%s = 0x%02x, want 0x%02x", tt.op, byte(tt.op), tt.expected)
		}
	}

	t.Logf("✓ All opcode byte values match expected values")
}

func TestPushOpCodeRange(t *testing.T) {
	// Verify PUSH opcodes are sequential
	for i := 0; i < 32; i++ {
		expected := OpCode(0x60 + i)
		actual := PUSH1 + OpCode(i)
		if actual != expected {
			t.Errorf("PUSH%d: got 0x%02x, want 0x%02x", i+1, byte(actual), byte(expected))
		}
	}

	t.Logf("✓ PUSH opcodes are sequential from 0x60 to 0x7f")
}

func TestDupOpCodeRange(t *testing.T) {
	// Verify DUP opcodes are sequential
	dups := []OpCode{
		DUP1, DUP2, DUP3, DUP4, DUP5, DUP6, DUP7, DUP8,
		DUP9, DUP10, DUP11, DUP12, DUP13, DUP14, DUP15, DUP16,
	}

	for i, dup := range dups {
		expected := OpCode(0x80 + i)
		if dup != expected {
			t.Errorf("DUP%d: got 0x%02x, want 0x%02x", i+1, byte(dup), byte(expected))
		}
	}

	t.Logf("✓ DUP opcodes are sequential from 0x80 to 0x8f")
}

func TestSwapOpCodeRange(t *testing.T) {
	// Verify SWAP opcodes are sequential
	swaps := []OpCode{
		SWAP1, SWAP2, SWAP3, SWAP4, SWAP5, SWAP6, SWAP7, SWAP8,
		SWAP9, SWAP10, SWAP11, SWAP12, SWAP13, SWAP14, SWAP15, SWAP16,
	}

	for i, swap := range swaps {
		expected := OpCode(0x90 + i)
		if swap != expected {
			t.Errorf("SWAP%d: got 0x%02x, want 0x%02x", i+1, byte(swap), byte(expected))
		}
	}

	t.Logf("✓ SWAP opcodes are sequential from 0x90 to 0x9f")
}

func TestLogOpCodeRange(t *testing.T) {
	// Verify LOG opcodes are sequential
	logs := []OpCode{LOG0, LOG1, LOG2, LOG3, LOG4}

	for i, log := range logs {
		expected := OpCode(0xa0 + i)
		if log != expected {
			t.Errorf("LOG%d: got 0x%02x, want 0x%02x", i, byte(log), byte(expected))
		}
	}

	t.Logf("✓ LOG opcodes are sequential from 0xa0 to 0xa4")
}

// =============================================================================
// OpCode Benchmark Tests
// =============================================================================

func BenchmarkOpCodeString(b *testing.B) {
	op := ADD
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = op.String()
	}
}

func BenchmarkStringToOp(b *testing.B) {
	name := "ADD"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StringToOp(name)
	}
}

func BenchmarkOpCodeIsPush(b *testing.B) {
	op := PUSH1
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = op.IsPush()
	}
}

func BenchmarkOpCodeIsStaticJump(b *testing.B) {
	op := JUMP
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = op.IsStaticJump()
	}
}

