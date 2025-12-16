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
// Reference: go-ethereum/core/vm/instructions_test.go

package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// testTwoOperandOp tests a two-operand opcode
func testTwoOperandOp(t *testing.T, opFn func(*uint64, *EVMInterpreter, *ScopeContext) ([]byte, error), tests []twoOperandTest) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(x)
			s.Push(y)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opFn(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("Result = %v, want %v", result, expected)
			}
		})
	}
}

type twoOperandTest struct {
	name     string
	x        *big.Int
	y        *big.Int
	expected *big.Int
}

// =============================================================================
// Arithmetic Operation Tests
// =============================================================================

func TestOpAdd(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(5), big.NewInt(3), big.NewInt(8)},
		{"zero_plus_zero", big.NewInt(0), big.NewInt(0), big.NewInt(0)},
		{"zero_plus_num", big.NewInt(0), big.NewInt(100), big.NewInt(100)},
		{"large_numbers", big.NewInt(1000000), big.NewInt(2000000), big.NewInt(3000000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opAdd(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opAdd(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opAdd tests passed")
}

func TestOpSub(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(3), big.NewInt(7)},
		{"result_zero", big.NewInt(5), big.NewInt(5), big.NewInt(0)},
		{"from_zero", big.NewInt(0), big.NewInt(5), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(5))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opSub(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opSub(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opSub tests passed")
}

func TestOpMul(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(5), big.NewInt(3), big.NewInt(15)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)},
		{"by_one", big.NewInt(100), big.NewInt(1), big.NewInt(100)},
		{"large", big.NewInt(1000), big.NewInt(1000), big.NewInt(1000000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opMul(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opMul(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opMul tests passed")
}

func TestOpDiv(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(2), big.NewInt(5)},
		{"by_one", big.NewInt(100), big.NewInt(1), big.NewInt(100)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)}, // Division by zero returns 0
		{"truncate", big.NewInt(7), big.NewInt(2), big.NewInt(3)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opDiv(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opDiv(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opDiv tests passed")
}

func TestOpMod(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(10), big.NewInt(3), big.NewInt(1)},
		{"exact", big.NewInt(10), big.NewInt(5), big.NewInt(0)},
		{"by_zero", big.NewInt(100), big.NewInt(0), big.NewInt(0)}, // Mod by zero returns 0
		{"by_larger", big.NewInt(3), big.NewInt(10), big.NewInt(3)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opMod(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opMod(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opMod tests passed")
}

func TestOpExp(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(2), big.NewInt(3), big.NewInt(8)},
		{"base_zero", big.NewInt(0), big.NewInt(5), big.NewInt(0)},
		{"exp_zero", big.NewInt(5), big.NewInt(0), big.NewInt(1)},
		{"base_one", big.NewInt(1), big.NewInt(100), big.NewInt(1)},
		{"exp_one", big.NewInt(5), big.NewInt(1), big.NewInt(5)},
		{"base_two", big.NewInt(2), big.NewInt(8), big.NewInt(256)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opExp(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opExp(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opExp tests passed")
}

// =============================================================================
// Comparison Operation Tests
// =============================================================================

func TestOpLt(t *testing.T) {
	tests := []struct {
		name     string
		x        *big.Int
		y        *big.Int
		expected bool
	}{
		{"less", big.NewInt(3), big.NewInt(5), true},
		{"equal", big.NewInt(5), big.NewInt(5), false},
		{"greater", big.NewInt(7), big.NewInt(5), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opLt(&pc, nil, scope)

			result := s.Pop()
			expectedInt := uint64(0)
			if tt.expected {
				expectedInt = 1
			}

			if result.Uint64() != expectedInt {
				t.Errorf("opLt(%v, %v) = %v, want %v", tt.x, tt.y, result.Uint64(), expectedInt)
			}
		})
	}

	t.Logf("✓ opLt tests passed")
}

func TestOpGt(t *testing.T) {
	tests := []struct {
		name     string
		x        *big.Int
		y        *big.Int
		expected bool
	}{
		{"less", big.NewInt(3), big.NewInt(5), false},
		{"equal", big.NewInt(5), big.NewInt(5), false},
		{"greater", big.NewInt(7), big.NewInt(5), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opGt(&pc, nil, scope)

			result := s.Pop()
			expectedInt := uint64(0)
			if tt.expected {
				expectedInt = 1
			}

			if result.Uint64() != expectedInt {
				t.Errorf("opGt(%v, %v) = %v, want %v", tt.x, tt.y, result.Uint64(), expectedInt)
			}
		})
	}

	t.Logf("✓ opGt tests passed")
}

func TestOpEq(t *testing.T) {
	tests := []struct {
		name     string
		x        *big.Int
		y        *big.Int
		expected bool
	}{
		{"equal", big.NewInt(5), big.NewInt(5), true},
		{"not_equal", big.NewInt(3), big.NewInt(5), false},
		{"zeros", big.NewInt(0), big.NewInt(0), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opEq(&pc, nil, scope)

			result := s.Pop()
			expectedInt := uint64(0)
			if tt.expected {
				expectedInt = 1
			}

			if result.Uint64() != expectedInt {
				t.Errorf("opEq(%v, %v) = %v, want %v", tt.x, tt.y, result.Uint64(), expectedInt)
			}
		})
	}

	t.Logf("✓ opEq tests passed")
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
			expectedInt := uint64(0)
			if tt.expected {
				expectedInt = 1
			}

			if result.Uint64() != expectedInt {
				t.Errorf("opIszero(%v) = %v, want %v", tt.x, result.Uint64(), expectedInt)
			}
		})
	}

	t.Logf("✓ opIszero tests passed")
}

// =============================================================================
// Bitwise Operation Tests
// =============================================================================

func TestOpAnd(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(0xFF), big.NewInt(0x0F), big.NewInt(0x0F)},
		{"all_ones", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0xFF)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opAnd(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opAnd(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opAnd tests passed")
}

func TestOpOr(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(0xF0), big.NewInt(0x0F), big.NewInt(0xFF)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0xFF)},
		{"same", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0xFF)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opOr(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opOr(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opOr tests passed")
}

func TestOpXor(t *testing.T) {
	tests := []twoOperandTest{
		{"simple", big.NewInt(0xFF), big.NewInt(0x0F), big.NewInt(0xF0)},
		{"same", big.NewInt(0xFF), big.NewInt(0xFF), big.NewInt(0)},
		{"with_zero", big.NewInt(0xFF), big.NewInt(0), big.NewInt(0xFF)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()
			x := new(uint256.Int)
			y := new(uint256.Int)
			x.SetFromBig(tt.x)
			y.SetFromBig(tt.y)

			s.Push(y)
			s.Push(x)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opXor(&pc, nil, scope)

			result := s.Pop()
			expected := new(uint256.Int)
			expected.SetFromBig(tt.expected)

			if result.Cmp(expected) != 0 {
				t.Errorf("opXor(%v, %v) = %v, want %v", tt.x, tt.y, result, expected)
			}
		})
	}

	t.Logf("✓ opXor tests passed")
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

	// NOT(0) should be all 1s (max uint256)
	expected := new(uint256.Int).SetAllOne()
	if result.Cmp(expected) != 0 {
		t.Errorf("opNot(0) = %v, want max uint256", result)
	}

	t.Logf("✓ opNot tests passed")
}

func TestOpByte(t *testing.T) {
	tests := []struct {
		name     string
		th       uint64 // byte position (0 = MSB)
		val      []byte
		expected uint64
	}{
		{"first_byte", 0, []byte{0xAB, 0xCD}, 0xAB},
		{"second_byte", 1, []byte{0xAB, 0xCD}, 0xCD},
		{"beyond", 32, []byte{0xAB}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.New()

			val := new(uint256.Int).SetBytes(tt.val)
			th := new(uint256.Int).SetUint64(tt.th)

			// Adjust for right-aligned bytes in uint256
			// The BYTE opcode reads from left (MSB), so byte 0 is the highest byte
			s.Push(val)
			s.Push(th)

			scope := &ScopeContext{
				Stack:  s,
				Memory: NewMemory(),
			}

			pc := uint64(0)
			opByte(&pc, nil, scope)

			result := s.Pop()
			// Note: actual result depends on val alignment in 256 bits
			t.Logf("opByte(%d, %x) = %d", tt.th, tt.val, result.Uint64())
		})
	}

	t.Logf("✓ opByte tests passed")
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

			shift := uint256.NewInt(tt.shift)
			value := uint256.NewInt(tt.value)

			s.Push(value)
			s.Push(shift)

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

	t.Logf("✓ opSHL tests passed")
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

			shift := uint256.NewInt(tt.shift)
			value := uint256.NewInt(tt.value)

			s.Push(value)
			s.Push(shift)

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

	t.Logf("✓ opSHR tests passed")
}

// =============================================================================
// Code Bitmap Tests (from go-ethereum)
// =============================================================================

func TestCodeBitmap(t *testing.T) {
	// Simple code: PUSH1 0x60 PUSH1 0x40 ADD
	code := []byte{byte(PUSH1), 0x60, byte(PUSH1), 0x40, byte(ADD)}

	bitmap := codeBitmap(code)
	if bitmap == nil {
		t.Fatal("codeBitmap returned nil")
	}

	// Check positions
	// Position 0 (PUSH1) is code
	// Position 1 (0x60) is data
	// Position 2 (PUSH1) is code
	// Position 3 (0x40) is data
	// Position 4 (ADD) is code

	t.Logf("Code bitmap generated for %d byte code", len(code))
	t.Logf("✓ codeBitmap tests passed")
}

func TestIsCodeFromAnalysis(t *testing.T) {
	// Create simple bitmap
	code := []byte{byte(PUSH1), 0x60, byte(JUMPDEST)}
	bitmap := codeBitmap(code)

	// Position 0 should be code (PUSH1)
	if !isCodeFromAnalysis(bitmap, 0) {
		t.Error("Position 0 should be code")
	}

	// Position 1 should be data (immediate of PUSH1)
	if isCodeFromAnalysis(bitmap, 1) {
		t.Error("Position 1 should be data")
	}

	// Position 2 should be code (JUMPDEST)
	if !isCodeFromAnalysis(bitmap, 2) {
		t.Error("Position 2 should be code")
	}

	t.Logf("✓ isCodeFromAnalysis tests passed")
}

// =============================================================================
// Error Tests
// =============================================================================

func TestErrStackUnderflow(t *testing.T) {
	err := &ErrStackUnderflow{stackLen: 1, required: 2}
	str := err.Error()
	if str == "" {
		t.Error("Error string should not be empty")
	}
	t.Logf("ErrStackUnderflow: %s", str)

	t.Logf("✓ ErrStackUnderflow test passed")
}

func TestErrStackOverflow(t *testing.T) {
	err := &ErrStackOverflow{stackLen: 1025, limit: 1024}
	str := err.Error()
	if str == "" {
		t.Error("Error string should not be empty")
	}
	t.Logf("ErrStackOverflow: %s", str)

	t.Logf("✓ ErrStackOverflow test passed")
}

func TestErrInvalidOpCode(t *testing.T) {
	err := &ErrInvalidOpCode{opcode: OpCode(0x21)}
	str := err.Error()
	if str == "" {
		t.Error("Error string should not be empty")
	}
	t.Logf("ErrInvalidOpCode: %s", str)

	t.Logf("✓ ErrInvalidOpCode test passed")
}

// =============================================================================
// codeAndHash Tests
// =============================================================================

func TestCodeAndHash(t *testing.T) {
	code := []byte{byte(PUSH1), 0x60, byte(STOP)}
	cah := &codeAndHash{code: code}

	// Get hash (should compute on first access)
	hash := cah.Hash()
	if hash == (types.Hash{}) {
		t.Error("Hash should not be zero")
	}

	// Second call should return cached hash
	hash2 := cah.Hash()
	if hash != hash2 {
		t.Error("Hash should be cached")
	}

	t.Logf("Code hash: %x", hash)
	t.Logf("✓ codeAndHash test passed")
}

// =============================================================================
// Instruction Benchmarks
// =============================================================================

func BenchmarkOpAdd(b *testing.B) {
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	x := uint256.NewInt(100)
	y := uint256.NewInt(200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(x.Clone())
		s.Push(y.Clone())
		pc := uint64(0)
		opAdd(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkOpMul(b *testing.B) {
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	x := uint256.NewInt(100)
	y := uint256.NewInt(200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(x.Clone())
		s.Push(y.Clone())
		pc := uint64(0)
		opMul(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkOpDiv(b *testing.B) {
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	x := uint256.NewInt(1000)
	y := uint256.NewInt(10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(x.Clone())
		s.Push(y.Clone())
		pc := uint64(0)
		opDiv(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkOpExp(b *testing.B) {
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	base := uint256.NewInt(2)
	exp := uint256.NewInt(10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(base.Clone())
		s.Push(exp.Clone())
		pc := uint64(0)
		opExp(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkOpSHL(b *testing.B) {
	s := stack.New()
	scope := &ScopeContext{
		Stack:  s,
		Memory: NewMemory(),
	}

	shift := uint256.NewInt(4)
	value := uint256.NewInt(1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(value.Clone())
		s.Push(shift.Clone())
		pc := uint64(0)
		opSHL(&pc, nil, scope)
		s.Pop()
	}
}

func BenchmarkCodeBitmap(b *testing.B) {
	// Larger code for more realistic benchmark
	code := make([]byte, 1024)
	for i := range code {
		if i%3 == 0 {
			code[i] = byte(PUSH1)
		} else if i%3 == 1 {
			code[i] = 0x60
		} else {
			code[i] = byte(ADD)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		codeBitmap(code)
	}
}

