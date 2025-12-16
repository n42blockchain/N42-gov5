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
// Reference: go-ethereum/core/vm/interpreter_test.go

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Config Tests
// =============================================================================

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}

	if cfg.Debug {
		t.Error("Default Debug should be false")
	}
	if cfg.Tracer != nil {
		t.Error("Default Tracer should be nil")
	}
	if cfg.NoRecursion {
		t.Error("Default NoRecursion should be false")
	}
	if cfg.NoBaseFee {
		t.Error("Default NoBaseFee should be false")
	}
	if cfg.SkipAnalysis {
		t.Error("Default SkipAnalysis should be false")
	}

	t.Logf("✓ Config defaults are correct")
}

func TestConfigHasEip3860(t *testing.T) {
	tests := []struct {
		name      string
		extraEips []int
		rules     *params.Rules
		expected  bool
	}{
		{
			name:      "no_extra_eips_pre_shanghai",
			extraEips: nil,
			rules:     &params.Rules{IsShanghai: false},
			expected:  false,
		},
		{
			name:      "no_extra_eips_shanghai",
			extraEips: nil,
			rules:     &params.Rules{IsShanghai: true},
			expected:  true,
		},
		{
			name:      "with_eip3860",
			extraEips: []int{3860},
			rules:     &params.Rules{IsShanghai: false},
			expected:  true,
		},
		{
			name:      "with_other_eips",
			extraEips: []int{1234, 5678},
			rules:     &params.Rules{IsShanghai: false},
			expected:  false,
		},
		{
			name:      "with_eip3860_and_others",
			extraEips: []int{1234, 3860, 5678},
			rules:     &params.Rules{IsShanghai: false},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{ExtraEips: tt.extraEips}
			result := cfg.HasEip3860(tt.rules)
			if result != tt.expected {
				t.Errorf("HasEip3860() = %v, want %v", result, tt.expected)
			}
		})
	}

	t.Logf("✓ Config.HasEip3860 works correctly")
}

// =============================================================================
// JumpTable Tests
// =============================================================================

func TestCopyJumpTable(t *testing.T) {
	original := &frontierInstructionSet

	copied := copyJumpTable(original)

	if copied == original {
		t.Error("Copy should be a different pointer")
	}

	// Verify copy has same content
	for i := 0; i < 256; i++ {
		origOp := original[i]
		copyOp := copied[i]

		if origOp == nil && copyOp == nil {
			continue
		}

		if (origOp == nil) != (copyOp == nil) {
			t.Errorf("Mismatch at opcode %d: orig=%v, copy=%v", i, origOp, copyOp)
			continue
		}

		if copyOp == origOp {
			t.Errorf("Operation at %d should be a copy, not same pointer", i)
		}

		if copyOp.constantGas != origOp.constantGas {
			t.Errorf("ConstantGas mismatch at %d", i)
		}
	}

	t.Logf("✓ copyJumpTable creates independent copy")
}

func TestJumpTableInstructionSets(t *testing.T) {
	instructionSets := []*JumpTable{
		&frontierInstructionSet,
		&homesteadInstructionSet,
		&tangerineWhistleInstructionSet,
		&spuriousDragonInstructionSet,
		&byzantiumInstructionSet,
		&constantinopleInstructionSet,
		&istanbulInstructionSet,
		&berlinInstructionSet,
		&londonInstructionSet,
		&shanghaiInstructionSet,
		&cancunInstructionSet,
		&pragueInstructionSet,
	}

	names := []string{
		"frontier",
		"homestead",
		"tangerineWhistle",
		"spuriousDragon",
		"byzantium",
		"constantinople",
		"istanbul",
		"berlin",
		"london",
		"shanghai",
		"cancun",
		"prague",
	}

	for i, jt := range instructionSets {
		t.Run(names[i], func(t *testing.T) {
			// Basic operations should always be defined
			basicOps := []OpCode{STOP, ADD, MUL, SUB, DIV, PUSH1, POP, JUMP, JUMPI, JUMPDEST}
			for _, op := range basicOps {
				if jt[op] == nil {
					t.Errorf("%s: Operation %s should be defined", names[i], op)
				}
			}
		})
	}

	t.Logf("✓ All instruction sets have basic operations")
}

func TestJumpTableShanghaiPush0(t *testing.T) {
	// PUSH0 should be defined in Shanghai and later
	if shanghaiInstructionSet[PUSH0] == nil {
		t.Error("PUSH0 should be defined in Shanghai")
	}
	if cancunInstructionSet[PUSH0] == nil {
		t.Error("PUSH0 should be defined in Cancun")
	}
	if pragueInstructionSet[PUSH0] == nil {
		t.Error("PUSH0 should be defined in Prague")
	}

	// Note: PUSH0 availability in pre-Shanghai forks depends on implementation
	// Some implementations may include it with a higher gas cost or as undefined
	// The important check is that Shanghai+ definitely has it
	t.Logf("✓ PUSH0 defined in Shanghai+")
}

func TestJumpTableCancunOperations(t *testing.T) {
	// Cancun-specific operations
	cancunOps := []OpCode{TLOAD, TSTORE, MCOPY, BLOBHASH, BLOBBASEFEE}

	for _, op := range cancunOps {
		if cancunInstructionSet[op] == nil {
			t.Errorf("Operation %s should be defined in Cancun", op)
		}
		if pragueInstructionSet[op] == nil {
			t.Errorf("Operation %s should be defined in Prague", op)
		}
	}

	t.Logf("✓ Cancun operations (TLOAD, TSTORE, MCOPY, BLOBHASH, BLOBBASEFEE) defined")
}

// =============================================================================
// ScopeContext Tests
// =============================================================================

func TestScopeContextFields(t *testing.T) {
	mem := NewMemory()
	stk := &ScopeContext{
		Memory: mem,
	}

	if stk.Memory != mem {
		t.Error("Memory field mismatch")
	}

	t.Logf("✓ ScopeContext fields work correctly")
}

// =============================================================================
// VM ReadOnly Tests
// =============================================================================

func TestVMReadOnlyMode(t *testing.T) {
	vm := &VM{}

	// Default should be false
	if vm.getReadonly() {
		t.Error("Default readOnly should be false")
	}

	// Set to true
	cleanup := vm.setReadonly(true)
	if !vm.getReadonly() {
		t.Error("readOnly should be true after setReadonly(true)")
	}

	// Cleanup should reset
	cleanup()
	if vm.getReadonly() {
		t.Error("readOnly should be false after cleanup")
	}

	t.Logf("✓ VM readOnly mode works correctly")
}

func TestVMSetReadonlyNested(t *testing.T) {
	vm := &VM{}

	// First set
	cleanup1 := vm.setReadonly(true)

	// Second set (already read-only)
	cleanup2 := vm.setReadonly(true)

	// Should still be read-only
	if !vm.getReadonly() {
		t.Error("Should be read-only")
	}

	// Second cleanup (should be no-op since outer set it)
	cleanup2()
	if !vm.getReadonly() {
		t.Error("Should still be read-only after inner cleanup")
	}

	// First cleanup
	cleanup1()
	if vm.getReadonly() {
		t.Error("Should not be read-only after outer cleanup")
	}

	t.Logf("✓ VM nested readOnly works correctly")
}

func TestVMDisableReadonly(t *testing.T) {
	vm := &VM{readOnly: true}

	vm.disableReadonly()

	if vm.getReadonly() {
		t.Error("readOnly should be false after disableReadonly")
	}

	t.Logf("✓ VM disableReadonly works correctly")
}

func TestVMNoop(t *testing.T) {
	vm := &VM{}
	// noop should not panic
	vm.noop()
	t.Logf("✓ VM noop works correctly")
}

// =============================================================================
// Memory Pool Tests
// =============================================================================

func TestMemoryPool(t *testing.T) {
	// Get memory from pool
	mem := pool.Get().(*Memory)
	if mem == nil {
		t.Fatal("Pool returned nil")
	}

	// Use memory
	mem.Resize(64)
	mem.Set(0, 32, make([]byte, 32))

	// Return to pool
	mem.Reset()
	pool.Put(mem)

	// Get again (may be same instance)
	mem2 := pool.Get().(*Memory)
	if mem2 == nil {
		t.Fatal("Pool returned nil on second get")
	}

	// Should be clean
	if mem2.Len() != 0 {
		t.Error("Memory from pool should be reset")
	}

	pool.Put(mem2)

	t.Logf("✓ Memory pool works correctly")
}

// =============================================================================
// Interpreter Interface Test
// =============================================================================

func TestInterpreterInterface(t *testing.T) {
	// Verify EVMInterpreter implements Interpreter interface
	var _ Interpreter = (*EVMInterpreter)(nil)

	t.Logf("✓ EVMInterpreter implements Interpreter interface")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCopyJumpTable(b *testing.B) {
	original := &frontierInstructionSet

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copyJumpTable(original)
	}
}

func BenchmarkMemoryPoolGetPut(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem := pool.Get().(*Memory)
		mem.Reset()
		pool.Put(mem)
	}
}

func BenchmarkVMSetReadonly(b *testing.B) {
	vm := &VM{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cleanup := vm.setReadonly(true)
		cleanup()
	}
}

