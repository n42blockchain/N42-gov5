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
//
// EVM evaluation stack backed by a []uint256.Int slice. Stack provides
// Push, PushN, Pop, Peek, Back and Swap primitives used by every
// opcode handler, with a sync.Pool (stackPool) in New that recycles
// pre-sized 16-entry stacks to avoid per-call allocations. Stack
// depth is capped at 1024 and enforced by the jump table's baseCheck
// (not by these methods directly).
package stack

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/log"
)

var stackPool = sync.Pool{
	New: func() any {
		return &Stack{Data: make([]uint256.Int, 0, 16)}
	},
}

// Stack is an object for basic stack operations. Items popped to the stack are
// expected to be changed and modified. stack does not take care of adding newly
// initialised objects.
type Stack struct {
	Data []uint256.Int
}

func New() *Stack {
	stack, ok := stackPool.Get().(*Stack)
	if !ok {
		log.Error("Type assertion failure", "err", "cannot get Stack pointer from stackPool")
		// Return a new Stack to prevent nil pointer dereference
		return &Stack{Data: make([]uint256.Int, 0, 16)}
	}
	return stack
}

func (st *Stack) Push(d *uint256.Int) {
	// NOTE push limit (1024) is checked in baseCheck
	st.Data = append(st.Data, *d)
}

// PushSlot appends a zero value and returns the new top slot. It lets opcode
// implementations initialize the destination in place instead of initializing
// a temporary uint256.Int and then copying it into the stack.
func (st *Stack) PushSlot() *uint256.Int {
	st.Data = append(st.Data, uint256.Int{})
	return &st.Data[len(st.Data)-1]
}

func (st *Stack) PushN(ds ...uint256.Int) {
	// Note: variadic parameters are passed by value in Go.
	// Changing to pointer-based approach would require API changes.
	st.Data = append(st.Data, ds...)
}

func (st *Stack) Pop() (ret uint256.Int) {
	// Security: bounds check before accessing - prevents panic on empty stack
	if len(st.Data) == 0 {
		log.Error("Stack underflow: Pop called on empty stack", "len", 0)
		return
	}
	ret = st.Data[len(st.Data)-1]
	st.Data = st.Data[:len(st.Data)-1]
	return
}

// PopPtr removes the top item and returns a pointer to it, avoiding the 32-byte
// value copy that Pop performs. The returned pointer aliases the just-vacated
// backing slot and is valid ONLY until the next Push, which overwrites that slot.
// Callers must therefore consume the value before pushing anything; every opcode
// that uses PopPtr (arithmetic/comparison/bitwise/shift/modular) folds the popped
// operand into the surviving Peek'd operand immediately and never pushes.
func (st *Stack) PopPtr() *uint256.Int {
	// Security: bounds check before accessing - prevents panic on empty stack
	if len(st.Data) == 0 {
		log.Error("Stack underflow: PopPtr called on empty stack", "len", 0)
		return new(uint256.Int)
	}
	ptr := &st.Data[len(st.Data)-1]
	st.Data = st.Data[:len(st.Data)-1]
	return ptr
}

func (st *Stack) Cap() int {
	return cap(st.Data)
}

func (st *Stack) Swap(n int) {
	// Security: bounds check before accessing - prevents panic on insufficient stack
	if n < 1 || st.Len() < n {
		log.Error("Stack underflow: Swap called with invalid n or insufficient stack", "n", n, "len", st.Len())
		return
	}
	st.Data[st.Len()-n], st.Data[st.Len()-1] = st.Data[st.Len()-1], st.Data[st.Len()-n]
}

func (st *Stack) Dup(n int) {
	// Security: bounds check before accessing - prevents panic on insufficient stack
	if n < 1 || st.Len() < n {
		log.Error("Stack underflow: Dup called with invalid n or insufficient stack", "n", n, "len", st.Len())
		return
	}
	st.Push(&st.Data[st.Len()-n])
}

// DupUnchecked is the interpreter fast path. The jump table validates stack
// depth before executing an opcode, so repeating logging-oriented bounds
// checks in every DUP only adds work to the hottest loop.
func (st *Stack) DupUnchecked(n int) {
	l := len(st.Data)
	st.Data = append(st.Data, st.Data[l-n])
}

// SwapUnchecked is the interpreter fast path; see DupUnchecked.
func (st *Stack) SwapUnchecked(n int) {
	l := len(st.Data)
	st.Data[l-n], st.Data[l-1] = st.Data[l-1], st.Data[l-n]
}

func (st *Stack) Peek() *uint256.Int {
	// Security: bounds check before accessing - prevents panic on empty stack
	if len(st.Data) == 0 {
		log.Error("Stack underflow: Peek called on empty stack", "len", 0)
		return nil
	}
	return &st.Data[st.Len()-1]
}

// Back returns the n'th item in stack
func (st *Stack) Back(n int) *uint256.Int {
	// Security: bounds check before accessing - prevents panic on insufficient stack
	if n < 0 || st.Len() <= n {
		log.Error("Stack underflow: Back called with invalid n or insufficient stack", "n", n, "len", st.Len())
		return nil
	}
	return &st.Data[st.Len()-n-1]
}

func (st *Stack) Reset() {
	st.Data = st.Data[:0]
}

func (st *Stack) Len() int {
	return len(st.Data)
}

// Print dumps the content of the stack
func (st *Stack) Print() {
	fmt.Println("### stack ###")
	if len(st.Data) > 0 {
		for i, val := range st.Data {
			fmt.Printf("%-3d  %v\n", i, val)
		}
	} else {
		fmt.Println("-- empty --")
	}
	fmt.Println("#############")
}

func ReturnNormalStack(s *Stack) {
	s.Data = s.Data[:0]
	stackPool.Put(s)
}

var rStackPool = sync.Pool{
	New: func() any {
		return &ReturnStack{data: make([]uint32, 0, 10)}
	},
}

func ReturnRStack(rs *ReturnStack) {
	rs.data = rs.data[:0]
	rStackPool.Put(rs)
}

// ReturnStack is an object for basic return stack operations.
type ReturnStack struct {
	data []uint32
}

func NewReturnStack() *ReturnStack {
	rStack, ok := rStackPool.Get().(*ReturnStack)
	if !ok {
		log.Error("Type assertion failure", "err", "cannot get ReturnStack pointer from rStackPool")
		// Return a new ReturnStack to prevent nil pointer dereference
		return &ReturnStack{data: make([]uint32, 0, 10)}
	}
	return rStack
}

func (st *ReturnStack) Push(d uint32) {
	st.data = append(st.data, d)
}

// A uint32 is sufficient as for code below 4.2G
func (st *ReturnStack) Pop() (ret uint32) {
	// Security: bounds check before accessing - prevents panic on empty stack
	if len(st.data) == 0 {
		log.Error("ReturnStack underflow: Pop called on empty stack", "len", 0)
		return
	}
	ret = st.data[len(st.data)-1]
	st.data = st.data[:len(st.data)-1]
	return
}

func (st *ReturnStack) Data() []uint32 {
	return st.data
}
