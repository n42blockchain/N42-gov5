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
		t.Error("default Debug should be false")
	}
	if cfg.Tracer != nil {
		t.Error("default Tracer should be nil")
	}
	if cfg.NoRecursion {
		t.Error("default NoRecursion should be false")
	}
	if cfg.NoBaseFee {
		t.Error("default NoBaseFee should be false")
	}
	if cfg.SkipAnalysis {
		t.Error("default SkipAnalysis should be false")
	}
}

func TestConfigHasEip3860(t *testing.T) {
	tests := []struct {
		name      string
		extraEips []int
		rules     *params.Rules
		expected  bool
	}{
		{
			name:     "pre_shanghai_no_extra_eips",
			rules:    &params.Rules{IsShanghai: false},
			expected: false,
		},
		{
			name:     "shanghai",
			rules:    &params.Rules{IsShanghai: true},
			expected: true,
		},
		{
			name:      "extra_eip_3860",
			extraEips: []int{3860},
			rules:     &params.Rules{IsShanghai: false},
			expected:  true,
		},
		{
			name:      "other_eips_only",
			extraEips: []int{1234, 5678},
			rules:     &params.Rules{IsShanghai: false},
			expected:  false,
		},
		{
			name:      "eip3860_among_others",
			extraEips: []int{1234, 3860, 5678},
			rules:     &params.Rules{IsShanghai: false},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{ExtraEips: tt.extraEips}
			if got := cfg.HasEip3860(tt.rules); got != tt.expected {
				t.Errorf("HasEip3860() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// =============================================================================
// JumpTable Tests
// =============================================================================

func TestCopyJumpTable(t *testing.T) {
	original := &frontierInstructionSet

	copied := copyJumpTable(original)

	if copied == original {
		t.Error("copy should be a different pointer")
	}

	for i := 0; i < 256; i++ {
		origOp := original[i]
		copyOp := copied[i]

		if origOp == nil && copyOp == nil {
			continue
		}
		if (origOp == nil) != (copyOp == nil) {
			t.Errorf("mismatch at opcode %d: orig=%v, copy=%v", i, origOp, copyOp)
			continue
		}
		if copyOp == origOp {
			t.Errorf("operation at %d should be a deep copy, not same pointer", i)
		}
		if copyOp.constantGas != origOp.constantGas {
			t.Errorf("constantGas mismatch at opcode %d", i)
		}
	}
}

func TestJumpTableInstructionSets(t *testing.T) {
	sets := map[string]*JumpTable{
		"frontier":         &frontierInstructionSet,
		"homestead":        &homesteadInstructionSet,
		"tangerineWhistle": &tangerineWhistleInstructionSet,
		"spuriousDragon":   &spuriousDragonInstructionSet,
		"byzantium":        &byzantiumInstructionSet,
		"constantinople":   &constantinopleInstructionSet,
		"istanbul":         &istanbulInstructionSet,
		"berlin":           &berlinInstructionSet,
		"london":           &londonInstructionSet,
		"shanghai":         &shanghaiInstructionSet,
		"cancun":           &cancunInstructionSet,
		"prague":           &pragueInstructionSet,
	}

	basicOps := []OpCode{STOP, ADD, MUL, SUB, DIV, PUSH1, POP, JUMP, JUMPI, JUMPDEST}

	for name, jt := range sets {
		t.Run(name, func(t *testing.T) {
			for _, op := range basicOps {
				if jt[op] == nil {
					t.Errorf("operation %s should be defined", op)
				}
			}
		})
	}
}

func TestJumpTableShanghaiPush0(t *testing.T) {
	forks := map[string]*JumpTable{
		"shanghai": &shanghaiInstructionSet,
		"cancun":   &cancunInstructionSet,
		"prague":   &pragueInstructionSet,
	}

	for name, jt := range forks {
		if jt[PUSH0] == nil {
			t.Errorf("PUSH0 should be defined in %s", name)
		}
	}
}

func TestJumpTableCancunOperations(t *testing.T) {
	cancunOps := []OpCode{TLOAD, TSTORE, MCOPY, BLOBHASH, BLOBBASEFEE}

	for _, op := range cancunOps {
		if cancunInstructionSet[op] == nil {
			t.Errorf("%s should be defined in Cancun", op)
		}
		if pragueInstructionSet[op] == nil {
			t.Errorf("%s should be defined in Prague", op)
		}
	}
}

// =============================================================================
// ScopeContext Tests
// =============================================================================

func TestScopeContextFields(t *testing.T) {
	mem := NewMemory()
	ctx := &ScopeContext{Memory: mem}

	if ctx.Memory != mem {
		t.Error("Memory field mismatch")
	}
}

// =============================================================================
// VM ReadOnly Tests
// =============================================================================

func TestVMReadOnlyMode(t *testing.T) {
	vm := &VM{}

	if vm.getReadonly() {
		t.Error("default readOnly should be false")
	}

	cleanup := vm.setReadonly(true)
	if !vm.getReadonly() {
		t.Error("readOnly should be true after setReadonly(true)")
	}

	cleanup()
	if vm.getReadonly() {
		t.Error("readOnly should be false after cleanup")
	}
}

func TestVMSetReadonlyNested(t *testing.T) {
	vm := &VM{}

	cleanup1 := vm.setReadonly(true)
	cleanup2 := vm.setReadonly(true)

	if !vm.getReadonly() {
		t.Error("should be read-only")
	}

	// Inner cleanup should be a no-op since outer set it first.
	cleanup2()
	if !vm.getReadonly() {
		t.Error("should still be read-only after inner cleanup")
	}

	cleanup1()
	if vm.getReadonly() {
		t.Error("should not be read-only after outer cleanup")
	}
}

func TestVMDisableReadonly(t *testing.T) {
	vm := &VM{readOnly: true}
	vm.disableReadonly()

	if vm.getReadonly() {
		t.Error("readOnly should be false after disableReadonly")
	}
}

func TestVMNoop(t *testing.T) {
	vm := &VM{}
	vm.noop() // should not panic
}

// =============================================================================
// Memory Pool Tests
// =============================================================================

func TestMemoryPool(t *testing.T) {
	mem := pool.Get().(*Memory)
	if mem == nil {
		t.Fatal("pool returned nil")
	}

	mem.Resize(64)
	_ = mem.Set(0, 32, make([]byte, 32))

	mem.Reset()
	pool.Put(mem)

	mem2 := pool.Get().(*Memory)
	if mem2 == nil {
		t.Fatal("pool returned nil on second get")
	}
	if mem2.Len() != 0 {
		t.Error("memory from pool should be reset")
	}

	pool.Put(mem2)
}

// =============================================================================
// Interpreter Interface Test
// =============================================================================

func TestInterpreterInterface(t *testing.T) {
	var _ Interpreter = (*EVMInterpreter)(nil)
}

// =============================================================================
// Benchmarks
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
