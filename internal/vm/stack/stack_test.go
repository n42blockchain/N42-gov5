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
		t.Fatal("New() should not return nil")
	}
	if s.Len() != 0 {
		t.Errorf("New stack should be empty, got len=%d", s.Len())
	}
	ReturnNormalStack(s)
	t.Logf("✓ Stack creation works correctly")
}

func TestStackPushPop(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	s.Push(val)

	if s.Len() != 1 {
		t.Errorf("Stack length should be 1, got %d", s.Len())
	}

	popped := s.Pop()
	if popped.Cmp(val) != 0 {
		t.Errorf("Popped value should be %v, got %v", val, popped)
	}

	if s.Len() != 0 {
		t.Errorf("Stack should be empty after pop, got len=%d", s.Len())
	}

	t.Logf("✓ Push/Pop works correctly")
}

func TestStackPushN(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	vals := []uint256.Int{*uint256.NewInt(1), *uint256.NewInt(2), *uint256.NewInt(3)}
	s.PushN(vals...)

	if s.Len() != 3 {
		t.Errorf("Stack length should be 3, got %d", s.Len())
	}

	// Pop in reverse order
	for i := len(vals) - 1; i >= 0; i-- {
		popped := s.Pop()
		if popped.Cmp(&vals[i]) != 0 {
			t.Errorf("Popped value should be %v, got %v", vals[i], popped)
		}
	}

	t.Logf("✓ PushN works correctly")
}

func TestStackPeek(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	val := uint256.NewInt(42)
	s.Push(val)

	peeked := s.Peek()
	if peeked.Cmp(val) != 0 {
		t.Errorf("Peeked value should be %v, got %v", val, peeked)
	}

	if s.Len() != 1 {
		t.Error("Peek should not change stack length")
	}

	t.Logf("✓ Peek works correctly")
}

func TestStackBack(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	// Back(0) should return the last element (3)
	back0 := s.Back(0)
	if back0.Uint64() != 3 {
		t.Errorf("Back(0) should be 3, got %v", back0)
	}

	// Back(1) should return the second-to-last element (2)
	back1 := s.Back(1)
	if back1.Uint64() != 2 {
		t.Errorf("Back(1) should be 2, got %v", back1)
	}

	// Back(2) should return the first element (1)
	back2 := s.Back(2)
	if back2.Uint64() != 1 {
		t.Errorf("Back(2) should be 1, got %v", back2)
	}

	t.Logf("✓ Back works correctly")
}

func TestStackSwap(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	// Swap top with 2nd from top (swap 3 and 2)
	s.Swap(2)

	if s.Peek().Uint64() != 2 {
		t.Errorf("After Swap(2), top should be 2, got %v", s.Peek())
	}

	s.Pop()
	if s.Peek().Uint64() != 3 {
		t.Errorf("After Swap(2), second should be 3, got %v", s.Peek())
	}

	t.Logf("✓ Swap works correctly")
}

func TestStackDup(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))

	// Dup(1) duplicates the top element
	s.Dup(1)

	if s.Len() != 3 {
		t.Errorf("After Dup(1), length should be 3, got %d", s.Len())
	}

	if s.Peek().Uint64() != 2 {
		t.Errorf("After Dup(1), top should be 2, got %v", s.Peek())
	}

	t.Logf("✓ Dup works correctly")
}

func TestStackReset(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	s.Push(uint256.NewInt(1))
	s.Push(uint256.NewInt(2))
	s.Push(uint256.NewInt(3))

	s.Reset()

	if s.Len() != 0 {
		t.Errorf("After Reset, length should be 0, got %d", s.Len())
	}

	t.Logf("✓ Reset works correctly")
}

func TestStackCap(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	cap := s.Cap()
	if cap < 16 {
		t.Errorf("Initial capacity should be at least 16, got %d", cap)
	}

	t.Logf("✓ Cap works correctly: %d", cap)
}

// =============================================================================
// ReturnStack Tests
// =============================================================================

func TestReturnStackNew(t *testing.T) {
	rs := NewReturnStack()
	if rs == nil {
		t.Fatal("NewReturnStack() should not return nil")
	}
	if len(rs.Data()) != 0 {
		t.Errorf("New return stack should be empty")
	}
	ReturnRStack(rs)
	t.Logf("✓ ReturnStack creation works correctly")
}

func TestReturnStackPushPop(t *testing.T) {
	rs := NewReturnStack()
	defer ReturnRStack(rs)

	rs.Push(42)

	if len(rs.Data()) != 1 {
		t.Errorf("ReturnStack length should be 1, got %d", len(rs.Data()))
	}

	popped := rs.Pop()
	if popped != 42 {
		t.Errorf("Popped value should be 42, got %d", popped)
	}

	if len(rs.Data()) != 0 {
		t.Errorf("ReturnStack should be empty after pop")
	}

	t.Logf("✓ ReturnStack Push/Pop works correctly")
}

func TestReturnStackData(t *testing.T) {
	rs := NewReturnStack()
	defer ReturnRStack(rs)

	rs.Push(1)
	rs.Push(2)
	rs.Push(3)

	data := rs.Data()
	if len(data) != 3 {
		t.Errorf("Data length should be 3, got %d", len(data))
	}

	expected := []uint32{1, 2, 3}
	for i, v := range data {
		if v != expected[i] {
			t.Errorf("Data[%d] should be %d, got %d", i, expected[i], v)
		}
	}

	t.Logf("✓ ReturnStack Data works correctly")
}

// =============================================================================
// Pool Tests
// =============================================================================

func TestStackPoolReuse(t *testing.T) {
	s1 := New()
	s1.Push(uint256.NewInt(42))
	ReturnNormalStack(s1)

	s2 := New()
	// s2 should be empty because we reset it before returning to pool
	if s2.Len() != 0 {
		t.Errorf("Reused stack should be empty, got len=%d", s2.Len())
	}
	ReturnNormalStack(s2)

	t.Logf("✓ Stack pool reuse works correctly")
}

func TestReturnStackPoolReuse(t *testing.T) {
	rs1 := NewReturnStack()
	rs1.Push(42)
	ReturnRStack(rs1)

	rs2 := NewReturnStack()
	if len(rs2.Data()) != 0 {
		t.Errorf("Reused return stack should be empty")
	}
	ReturnRStack(rs2)

	t.Logf("✓ ReturnStack pool reuse works correctly")
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestStackLargeValues(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	// Test with max uint256 value
	maxVal := new(uint256.Int).SetAllOne()
	s.Push(maxVal)

	popped := s.Pop()
	if popped.Cmp(maxVal) != 0 {
		t.Errorf("Large value not preserved correctly")
	}

	t.Logf("✓ Large values handled correctly")
}

func TestStackManyPushPop(t *testing.T) {
	s := New()
	defer ReturnNormalStack(s)

	n := 1000
	for i := 0; i < n; i++ {
		s.Push(uint256.NewInt(uint64(i)))
	}

	if s.Len() != n {
		t.Errorf("Stack length should be %d, got %d", n, s.Len())
	}

	for i := n - 1; i >= 0; i-- {
		popped := s.Pop()
		if popped.Uint64() != uint64(i) {
			t.Errorf("Popped value should be %d, got %v", i, popped)
		}
	}

	t.Logf("✓ Many Push/Pop operations work correctly")
}

// =============================================================================
// Benchmark Tests
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
