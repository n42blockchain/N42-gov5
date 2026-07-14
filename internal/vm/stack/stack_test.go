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

package stack

import (
	"testing"

	"github.com/holiman/uint256"
)

// =============================================================================
// Stack Basic Tests
// =============================================================================

func TestStackNew(t *testing.T) {
	s := New()
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.Len() != 0 {
		t.Errorf("new stack Len() = %d, want 0", s.Len())
	}
	ReturnNormalStack(s)
}

func TestStackPushPop(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	s.Push(val)

	if s.Len() != 1 {
		t.Errorf("after Push, Len() = %d, want 1", s.Len())
	}

	popped := s.Pop()
	if popped.Cmp(val) != 0 {
		t.Errorf("Pop() = %v, want %v", popped, val)
	}
	if s.Len() != 0 {
		t.Errorf("after Pop, Len() = %d, want 0", s.Len())
	}
}

func TestStackPopPtr(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(7))
	s.Push(uint256.NewInt(42))

	// PopPtr returns the top value and shrinks the stack, like Pop.
	p := s.PopPtr()
	if p.Uint64() != 42 {
		t.Errorf("PopPtr() = %v, want 42", p)
	}
	if s.Len() != 1 {
		t.Errorf("after PopPtr, Len() = %d, want 1", s.Len())
	}
	if s.Peek().Uint64() != 7 {
		t.Errorf("new top = %v, want 7", s.Peek())
	}
}

// TestStackPopPtrArithmeticOrder mirrors how the arithmetic opcodes consume the
// stack: `x, y := PopPtr(), Peek()` must yield x=old-top, y=new-top as distinct
// live slots, so folding x into y (y = y - x) matches Pop()+Peek() semantics.
func TestStackPopPtrArithmeticOrder(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(10)) // becomes y (second-from-top)
	s.Push(uint256.NewInt(3))  // becomes x (top)

	x, y := s.PopPtr(), s.Peek()
	if x.Uint64() != 3 || y.Uint64() != 10 {
		t.Fatalf("x=%v y=%v, want x=3 y=10", x, y)
	}
	y.Sub(y, x) // SUB: second - top = 10 - 3
	if s.Len() != 1 || s.Peek().Uint64() != 7 {
		t.Errorf("result top = %v (len %d), want 7 (len 1)", s.Peek(), s.Len())
	}
}

// TestStackPopPtrAliasingContract documents the invariant every caller relies on:
// the pointer aliases the vacated slot and stays valid only until the next Push,
// which overwrites it. Opcodes always consume the value before any Push.
func TestStackPopPtrAliasingContract(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(99))
	p := s.PopPtr()
	if p.Uint64() != 99 {
		t.Fatalf("PopPtr() = %v, want 99", p)
	}
	// A subsequent Push reuses the backing slot the pointer aliases.
	s.Push(uint256.NewInt(123))
	if p.Uint64() != 123 {
		t.Errorf("after Push, aliased slot = %v, want 123 (backing slot must be reused)", p)
	}
}

func TestStackPopPtrUnderflowSafe(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	// Must not panic and must not return nil on empty stack.
	p := s.PopPtr()
	if p == nil {
		t.Fatal("PopPtr() on empty stack returned nil")
	}
	if !p.IsZero() {
		t.Errorf("PopPtr() underflow = %v, want 0", p)
	}
}

func TestStackPushN(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	vals := []uint256.Int{*uint256.NewInt(1), *uint256.NewInt(2), *uint256.NewInt(3)}
	s.PushN(vals...)

	if s.Len() != 3 {
		t.Errorf("after PushN, Len() = %d, want 3", s.Len())
	}

	for i := len(vals) - 1; i >= 0; i-- {
		popped := s.Pop()
		if popped.Cmp(&vals[i]) != 0 {
			t.Errorf("Pop() = %v, want %v", popped, vals[i])
		}
	}
}

func TestStackPeek(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	s.Push(val)

	peeked := s.Peek()
	if peeked.Cmp(val) != 0 {
		t.Errorf("Peek() = %v, want %v", peeked, val)
	}
	if s.Len() != 1 {
		t.Error("Peek should not change stack length")
	}
}

func TestStackBack(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	tests := []struct {
		n    int
		want uint64
	}{
		{0, 3}, // Back(0) = top element
		{1, 2},
		{2, 1},
	}

	for _, tt := range tests {
		if got := s.Back(tt.n).Uint64(); got != tt.want {
			t.Errorf("Back(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestStackSwap(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	s.Swap(2)

	if s.Peek().Uint64() != 2 {
		t.Errorf("after Swap(2), top = %v, want 2", s.Peek())
	}
	s.Pop()
	if s.Peek().Uint64() != 3 {
		t.Errorf("after Swap(2), second = %v, want 3", s.Peek())
	}
}

func TestStackDup(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))

	s.Dup(1)

	if s.Len() != 3 {
		t.Errorf("after Dup(1), Len() = %d, want 3", s.Len())
	}
	if s.Peek().Uint64() != 2 {
		t.Errorf("after Dup(1), top = %v, want 2", s.Peek())
	}
}

func TestStackReset(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	s.Reset()

	if s.Len() != 0 {
		t.Errorf("after Reset, Len() = %d, want 0", s.Len())
	}
}

func TestStackCap(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	if c := s.Cap(); c < 16 {
		t.Errorf("initial Cap() = %d, want >= 16", c)
	}
}

// =============================================================================
// ReturnStack Tests
// =============================================================================

func TestReturnStackPushPop(t *testing.T) {
	rs := NewReturnStack()
	defer ReturnRStack(rs)

	if len(rs.Data()) != 0 {
		t.Errorf("new return stack Data() len = %d, want 0", len(rs.Data()))
	}

	rs.Push(42)
	if len(rs.Data()) != 1 {
		t.Errorf("after Push, Data() len = %d, want 1", len(rs.Data()))
	}

	popped := rs.Pop()
	if popped != 42 {
		t.Errorf("Pop() = %d, want 42", popped)
	}
	if len(rs.Data()) != 0 {
		t.Errorf("after Pop, Data() len = %d, want 0", len(rs.Data()))
	}
}

func TestReturnStackData(t *testing.T) {
	rs := NewReturnStack()
	defer ReturnRStack(rs)

	rs.Push(1)
	rs.Push(2)
	rs.Push(3)

	data := rs.Data()
	expected := []uint32{1, 2, 3}
	if len(data) != len(expected) {
		t.Fatalf("Data() len = %d, want %d", len(data), len(expected))
	}
	for i, v := range data {
		if v != expected[i] {
			t.Errorf("Data()[%d] = %d, want %d", i, v, expected[i])
		}
	}
}

// =============================================================================
// Pool Tests
// =============================================================================

func TestStackPoolReuse(t *testing.T) {
	s1 := New()
	s1.Push(uint256.NewInt(42))
	ReturnNormalStack(s1)

	s2 := New()
	if s2.Len() != 0 {
		t.Errorf("reused stack Len() = %d, want 0", s2.Len())
	}
	ReturnNormalStack(s2)
}

func TestReturnStackPoolReuse(t *testing.T) {
	rs1 := NewReturnStack()
	rs1.Push(42)
	ReturnRStack(rs1)

	rs2 := NewReturnStack()
	if len(rs2.Data()) != 0 {
		t.Errorf("reused return stack Data() len = %d, want 0", len(rs2.Data()))
	}
	ReturnRStack(rs2)
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestStackLargeValues(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	maxVal := new(uint256.Int).SetAllOne()
	s.Push(maxVal)

	popped := s.Pop()
	if popped.Cmp(maxVal) != 0 {
		t.Error("max uint256 value not preserved correctly")
	}
}

func TestStackManyPushPop(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	n := 1000
	for i := 0; i < n; i++ {
		s.Push(uint256.NewInt(uint64(i)))
	}

	if s.Len() != n {
		t.Errorf("after %d pushes, Len() = %d", n, s.Len())
	}

	for i := n - 1; i >= 0; i-- {
		popped := s.Pop()
		if popped.Uint64() != uint64(i) {
			t.Errorf("Pop() = %v, want %d", popped, i)
		}
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkStackPush(b *testing.B) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Push(val)
		if s.Len() > 100 {
			s.Reset()
		}
	}
}

func BenchmarkStackPop(b *testing.B) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	for i := 0; i < 1000; i++ {
		s.Push(val)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.Len() == 0 {
			for j := 0; j < 1000; j++ {
				s.Push(val)
			}
		}
		s.Pop()
	}
}

func BenchmarkStackPeek(b *testing.B) {
	s := New()
	defer ReturnNormalStack(s)
	s.Push(uint256.NewInt(42))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Peek()
	}
}

func BenchmarkStackSwap(b *testing.B) {
	s := New()
	defer ReturnNormalStack(s)

	for i := 0; i < 10; i++ {
		s.Push(uint256.NewInt(uint64(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Swap(5)
	}
}

func BenchmarkStackDup(b *testing.B) {
	s := New()
	defer ReturnNormalStack(s)
	s.Push(uint256.NewInt(42))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Dup(1)
		if s.Len() > 100 {
			s.Reset()
			s.Push(uint256.NewInt(42))
		}
	}
}

func BenchmarkStackNewReturn(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := New()
		ReturnNormalStack(s)
	}
}

func BenchmarkReturnStackPushPop(b *testing.B) {
	rs := NewReturnStack()
	defer ReturnRStack(rs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs.Push(42)
		if len(rs.Data()) > 100 {
			for len(rs.Data()) > 0 {
				rs.Pop()
			}
		}
	}
}
