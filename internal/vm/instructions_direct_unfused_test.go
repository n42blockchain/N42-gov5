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
// These tests call instructions.go's opcode execute functions directly,
// bypassing the interpreter dispatch loop entirely. This matters for JUMP
// and JUMPI in particular: a PUSH1/PUSH2 immediately followed by JUMP/JUMPI
// is rewritten by fuse.go's execView into a fused opFusedPushNJump(i)
// opcode when the contract is eligible (see canFuse), so driving the same
// pattern through EVM.Call exercises the fused path, not opJump/opJumpi
// themselves. Calling the execute functions directly is the only way to
// pin their own behavior independent of that optimization.

package vm

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/internal/vm/stack"
)

func vmTDirectScope(code []byte) *ScopeContext {
	return &ScopeContext{
		Stack:    stack.New(),
		Memory:   NewMemory(),
		Contract: &Contract{Code: code},
	}
}

func TestVMTOpJumpDirect(t *testing.T) {
	code := []byte{byte(JUMPDEST)}
	scope := vmTDirectScope(code)
	scope.Contract.skipAnalysis = true
	scope.Stack.Push(uint256.NewInt(0))

	pc := uint64(5)
	if _, err := opJump(&pc, nil, scope); err != nil {
		t.Fatalf("opJump failed: %v", err)
	}
	// opJump sets *pc = dest-1 because the interpreter loop increments pc.
	if pc != ^uint64(0) {
		t.Fatalf("opJump: pc = %d, want %d (0-1 wrapped)", pc, ^uint64(0))
	}
}

func TestVMTOpJumpDirectInvalidDest(t *testing.T) {
	code := []byte{byte(STOP)} // not a JUMPDEST
	scope := vmTDirectScope(code)
	scope.Contract.skipAnalysis = true
	scope.Stack.Push(uint256.NewInt(0))

	pc := uint64(0)
	if _, err := opJump(&pc, nil, scope); err != ErrInvalidJump {
		t.Fatalf("expected ErrInvalidJump, got %v", err)
	}
}

func TestVMTOpJumpiDirectTaken(t *testing.T) {
	code := []byte{byte(JUMPDEST)}
	scope := vmTDirectScope(code)
	scope.Contract.skipAnalysis = true
	// opJumpi pops pos then cond: push cond first (bottom), pos last (top).
	scope.Stack.Push(uint256.NewInt(1)) // cond (pushed first -> popped second)
	scope.Stack.Push(uint256.NewInt(0)) // pos (pushed last -> popped first)

	pc := uint64(9)
	if _, err := opJumpi(&pc, nil, scope); err != nil {
		t.Fatalf("opJumpi failed: %v", err)
	}
	if pc != ^uint64(0) {
		t.Fatalf("opJumpi (taken): pc = %d, want %d", pc, ^uint64(0))
	}
}

func TestVMTOpJumpiDirectNotTaken(t *testing.T) {
	code := []byte{byte(JUMPDEST)}
	scope := vmTDirectScope(code)
	scope.Contract.skipAnalysis = true
	scope.Stack.Push(uint256.NewInt(0)) // cond = false
	scope.Stack.Push(uint256.NewInt(0)) // pos

	pc := uint64(9)
	if _, err := opJumpi(&pc, nil, scope); err != nil {
		t.Fatalf("opJumpi failed: %v", err)
	}
	if pc != 9 {
		t.Fatalf("opJumpi (not taken): pc changed unexpectedly to %d", pc)
	}
}

func TestVMTOpPopDirect(t *testing.T) {
	scope := vmTDirectScope(nil)
	scope.Stack.Push(uint256.NewInt(1))
	scope.Stack.Push(uint256.NewInt(2))
	pc := uint64(0)
	if _, err := opPop(&pc, nil, scope); err != nil {
		t.Fatalf("opPop failed: %v", err)
	}
	if scope.Stack.Len() != 1 {
		t.Fatalf("expected 1 item left on stack, got %d", scope.Stack.Len())
	}
}

func TestVMTOpMloadMstoreDirect(t *testing.T) {
	scope := vmTDirectScope(nil)
	scope.Memory.Resize(32)
	scope.Stack.Push(uint256.NewInt(123))
	scope.Stack.Push(uint256.NewInt(0))
	pc := uint64(0)
	if _, err := opMstore(&pc, nil, scope); err != nil {
		t.Fatalf("opMstore failed: %v", err)
	}
	scope.Stack.Push(uint256.NewInt(0))
	if _, err := opMload(&pc, nil, scope); err != nil {
		t.Fatalf("opMload failed: %v", err)
	}
	got := scope.Stack.Pop()
	if got.Uint64() != 123 {
		t.Fatalf("opMload after opMstore: got %d, want 123", got.Uint64())
	}
}

func TestVMTOpSltDirect(t *testing.T) {
	scope := vmTDirectScope(nil)
	negOne := new(uint256.Int).Not(uint256.NewInt(0)) // -1
	scope.Stack.Push(uint256.NewInt(1))
	scope.Stack.Push(negOne)
	pc := uint64(0)
	if _, err := opSlt(&pc, nil, scope); err != nil {
		t.Fatalf("opSlt failed: %v", err)
	}
	got := scope.Stack.Pop()
	if got.Uint64() != 1 {
		t.Fatalf("opSlt(negOne, 1) = %d, want 1 (negOne < 1 signed)", got.Uint64())
	}
}

func TestVMTOpSgtDirect(t *testing.T) {
	scope := vmTDirectScope(nil)
	negOne := new(uint256.Int).Not(uint256.NewInt(0)) // -1
	scope.Stack.Push(negOne)
	scope.Stack.Push(uint256.NewInt(1))
	pc := uint64(0)
	if _, err := opSgt(&pc, nil, scope); err != nil {
		t.Fatalf("opSgt failed: %v", err)
	}
	got := scope.Stack.Pop()
	if got.Uint64() != 1 {
		t.Fatalf("opSgt(1, negOne) = %d, want 1 (1 > negOne signed)", got.Uint64())
	}
}
