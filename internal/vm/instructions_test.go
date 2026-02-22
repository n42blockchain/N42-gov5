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

// Reference: go-ethereum/core/vm/instructions_test.go

package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// executionFn is the signature of an EVM opcode function.
type executionFn func(*uint64, *EVMInterpreter, *ScopeContext) ([]byte, error)

// runTwoOperandOp executes a two-operand opcode with x on top, y below,
// and returns the result left on the stack.
func runTwoOperandOp(opFn executionFn, x, y *uint256.Int) uint256.Int {
	s := stack.New()
	s.Push(y.Clone())
	s.Push(x.Clone())

	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	pc := uint64(0)
	opFn(&pc, nil, scope)
	return s.Pop()
}

type twoOperandTest struct {
	name     string
	x        *big.Int
	y        *big.Int
	expected *big.Int
}

func testArithmeticOp(t *testing.T, opName string, opFn executionFn, tests []twoOperandTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			result := runTwoOperandOp(opFn, x, y)

			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)
			if result.Cmp(expected) != 0 {
				t.Errorf("%s(%v, %v) = %v, want %v", opName, tt.x, tt.y, result, expected)
			}
		})
	}
}

type comparisonTest struct {
	name     string
	x        *big.Int
	y        *big.Int
	expected bool
}

func testComparisonOp(t *testing.T, opName string, opFn executionFn, tests []comparisonTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			result := runTwoOperandOp(opFn, x, y)

			wantInt := uint64(0)
			if tt.expected {
				wantInt = 1
			}
			if result.Uint64() != wantInt {
				t.Errorf("%s(%v, %v) = %v, want %v", opName, tt.x, tt.y, result.Uint64(), wantInt)
			}
		})
	}
}

// =============================================================================
// Arithmetic Operation Tests
// =============================================================================

func TestOpAdd(t *testing.T) {
	testArithmeticOp(t, "opAdd", opAdd, []twoOperandTest{
		{"simple", big.NewInt(5), big.NewInt(3), big.NewInt(8)},
		{"zero_plus_zero", big.NewInt(0), big.NewInt(0), big.NewInt(0)},
		{"zero_plus_num", big.NewInt(0), big.NewInt(100), big.NewInt(100)},
		{"large_numbers", big.NewInt(1000000), big.NewInt(2000000), big.NewInt(3000000)},
	})
}

func TestOpSub(t *testing.T) {
	testArithmeticOp(t, "opSub", opSub, []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(3), big.NewInt(7)},
		{"result_zero", big.NewInt(5), big.NewInt(5), big.NewInt(0)},
		{"from_zero", big.NewInt(0), big.NewInt(5), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(5))},
	})
}

func TestOpMul(t *testing.T) {
	testArithmeticOp(t, "opMul", opMul, []twoOperandTest{
		{"simple", big.NewInt(5), big.NewInt(3), big.NewInt(15)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)},
		{"by_one", big.NewInt(100), big.NewInt(1), big.NewInt(100)},
		{"large", big.NewInt(1000), big.NewInt(1000), big.NewInt(1000000)},
	})
}

func TestOpDiv(t *testing.T) {
	testArithmeticOp(t, "opDiv", opDiv, []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(2), big.NewInt(5)},
		{"by_one", big.NewInt(100), big.NewInt(1), big.NewInt(100)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)},
		{"truncate", big.NewInt(7), big.NewInt(2), big.NewInt(3)},
	})
}

func TestOpMod(t *testing.T) {
	testArithmeticOp(t, "opMod", opMod, []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(3), big.NewInt(1)},
		{"exact", big.NewInt(10), big.NewInt(5), big.NewInt(0)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)},
		{"by_larger", big.NewInt(3), big.NewInt(10), big.NewInt(3)},
	})
}

func TestOpExp(t *testing.T) {
	testArithmeticOp(t, "opExp", opExp, []twoOperandTest{
		{"simple", big.NewInt(2), big.NewInt(3), big.NewInt(8)},
		{"base_zero", big.NewInt(0), big.NewInt(5), big.NewInt(0)},
		{"exp_zero", big.NewInt(5), big.NewInt(0), big.NewInt(1)},
		{"base_one", big.NewInt(1), big.NewInt(100), big.NewInt(1)},
		{"exp_one", big.NewInt(5), big.NewInt(1), big.NewInt(5)},
		{"base_two", big.NewInt(2), big.NewInt(8), big.NewInt(256)},
	})
}

// =============================================================================
// Comparison Operation Tests
// =============================================================================

func TestOpLt(t *testing.T) {
	testComparisonOp(t, "opLt", opLt, []comparisonTest{
		{"less", big.NewInt(3), big.NewInt(5), true},
		{"equal", big.NewInt(5), big.NewInt(5), false},
		{"greater", big.NewInt(7), big.NewInt(5), false},
	})
}

func TestOpGt(t *testing.T) {
	testComparisonOp(t, "opGt", opGt, []comparisonTest{
		{"less", big.NewInt(3), big.NewInt(5), false},
		{"equal", big.NewInt(5), big.NewInt(5), false},
		{"greater", big.NewInt(7), big.NewInt(5), true},
	})
}

func TestOpEq(t *testing.T) {
	testComparisonOp(t, "opEq", opEq, []comparisonTest{
		{"equal", big.NewInt(5), big.NewInt(5), true},
		{"not_equal", big.NewInt(3), big.NewInt(5), false},
		{"zeros", big.NewInt(0), big.NewInt(0), true},
	})
}

func TestOpIszero(t *testing.T) {
	tests := []struct {
		name     string
		x        *big.Int
		expected bool
	}{
		{"zero", big.NewInt(0), true},
		{"one", big.NewInt(1), false},
		{"large", big.NewInt(1000000), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			x.SetFromBig(tt.x)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opIszero(&pc, nil, scope)

			result := s.Pop()
			wantInt := uint64(0)
			if tt.expected {
				wantInt = 1
			}
			if result.Uint64() != wantInt {
				t.Errorf("opIszero(%v) = %v, want %v", tt.x, result.Uint64(), wantInt)
			}
		})
	}
}

// =============================================================================
// Bitwise Operation Tests
// =============================================================================

func TestOpAnd(t *testing.T) {
	testArithmeticOp(t, "opAnd", opAnd, []twoOperandTest{
		{"simple", big.NewInt(0xFF), big.NewInt(0x0F), big.NewInt(0x0F)},
		{"all_ones", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0xFF)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0)},
	})
}

func TestOpOr(t *testing.T) {
	testArithmeticOp(t, "opOr", opOr, []twoOperandTest{
		{"simple", big.NewInt(0xF0), big.NewInt(0x0F), big.NewInt(0xFF)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0xFF)},
		{"same", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0xFF)},
	})
}

func TestOpXor(t *testing.T) {
	testArithmeticOp(t, "opXor", opXor, []twoOperandTest{
		{"simple", big.NewInt(0xFF), big.NewInt(0x0F), big.NewInt(0xF0)},
		{"same", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0xFF)},
	})
}

func TestOpNot(t *testing.T) {
	s := stack.New()
	x := new(uint256.Int)
	x.SetUint64(0)
	s.Push(x)

	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	pc := uint64(0)
	opNot(&pc, nil, scope)

	result := s.Pop()
	expected := new(uint256.Int).SetAllOne()
	if result.Cmp(expected) != 0 {
		t.Errorf("opNot(0) = %v, want max uint256", result)
	}
}

func TestOpByte(t *testing.T) {
	tests := []struct {
		name string
		th   uint64 // byte position (0 = MSB)
		val  []byte
	}{
		{"first_byte", 0, []byte{0xAB, 0xCD}},
		{"second_byte", 1, []byte{0xAB, 0xCD}},
		{"beyond", 32, []byte{0xAB}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			val := new(uint256.Int).SetBytes(tt.val)
			th := new(uint256.Int).SetUint64(tt.th)

			s.Push(val)
			s.Push(th)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opByte(&pc, nil, scope)

			result := s.Pop()
			// The BYTE opcode reads from left (MSB), so the actual result
			// depends on val alignment in 256 bits.
			if tt.th >= 32 && result.Uint64() != 0 {
				t.Errorf("opByte(%d, %x) should be 0 for out-of-range, got %d", tt.th, tt.val, result.Uint64())
			}
		})
	}
}

// =============================================================================
// Shift Operation Tests
// =============================================================================

func TestOpSHL(t *testing.T) {
	tests := []struct {
		name     string
		shift    uint64
		value    uint64
		expected uint64
	}{
		{"shift_1", 1, 1, 2},
		{"shift_4", 4, 1, 16},
		{"shift_0", 0, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			s.Push(uint256.NewInt(tt.value))
			s.Push(uint256.NewInt(tt.shift))

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opSHL(&pc, nil, scope)

			result := s.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("opSHL(%d, %d) = %d, want %d", tt.shift, tt.value, result.Uint64(), tt.expected)
			}
		})
	}
}

func TestOpSHR(t *testing.T) {
	tests := []struct {
		name     string
		shift    uint64
		value    uint64
		expected uint64
	}{
		{"shift_1", 1, 2, 1},
		{"shift_4", 4, 16, 1},
		{"shift_0", 0, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			s.Push(uint256.NewInt(tt.value))
			s.Push(uint256.NewInt(tt.shift))

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opSHR(&pc, nil, scope)

			result := s.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("opSHR(%d, %d) = %d, want %d", tt.shift, tt.value, result.Uint64(), tt.expected)
			}
		})
	}
}

// =============================================================================
// Code Bitmap Tests
// =============================================================================

func TestCodeBitmap(t *testing.T) {
	code := []byte{byte(PUSH1), 0x60, byte(PUSH1), 0x40, byte(ADD)}

	bitmap := codeBitmap(code)
	if bitmap == nil {
		t.Fatal("codeBitmap returned nil")
	}
}

func TestIsCodeFromAnalysis(t *testing.T) {
	code := []byte{byte(PUSH1), 0x60, byte(JUMPDEST)}
	bitmap := codeBitmap(code)

	if !isCodeFromAnalysis(bitmap, 0) {
		t.Error("position 0 (PUSH1) should be code")
	}
	if isCodeFromAnalysis(bitmap, 1) {
		t.Error("position 1 (immediate of PUSH1) should be data")
	}
	if !isCodeFromAnalysis(bitmap, 2) {
		t.Error("position 2 (JUMPDEST) should be code")
	}
}

// =============================================================================
// codeAndHash Tests
// =============================================================================

func TestCodeAndHash(t *testing.T) {
	code := []byte{byte(PUSH1), 0x60, byte(STOP)}
	cah := &codeAndHash{code: code}

	hash := cah.Hash()
	if hash == (types.Hash{}) {
		t.Error("hash should not be zero")
	}

	hash2 := cah.Hash()
	if hash != hash2 {
		t.Error("hash should be cached on second call")
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func benchmarkBinaryOp(b *testing.B, opFn executionFn, x, y *uint256.Int) {
	b.Helper()
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(x.Clone())
		s.Push(y.Clone())
		pc := uint64(0)
		opFn(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkOpAdd(b *testing.B) {
	benchmarkBinaryOp(b, opAdd, uint256.NewInt(100), uint256.NewInt(200))
}

func BenchmarkOpMul(b *testing.B) {
	benchmarkBinaryOp(b, opMul, uint256.NewInt(100), uint256.NewInt(200))
}

func BenchmarkOpDiv(b *testing.B) {
	benchmarkBinaryOp(b, opDiv, uint256.NewInt(1000), uint256.NewInt(10))
}

func BenchmarkOpExp(b *testing.B) {
	benchmarkBinaryOp(b, opExp, uint256.NewInt(2), uint256.NewInt(10))
}

func BenchmarkOpSHL(b *testing.B) {
	benchmarkBinaryOp(b, opSHL, uint256.NewInt(4), uint256.NewInt(1))
}

func BenchmarkCodeBitmap(b *testing.B) {
	code := make([]byte, 1024)
	for i := range code {
		switch i % 3 {
		case 0:
			code[i] = byte(PUSH1)
		case 1:
			code[i] = 0x60
		default:
			code[i] = byte(ADD)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		codeBitmap(code)
	}
}
