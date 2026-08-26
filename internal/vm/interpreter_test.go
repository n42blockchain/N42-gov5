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
// Run Frame Tests
// =============================================================================

// TestRunFrameReusePerDepth: the interpreter owns one memory/stack/scope per
// call depth. The same depth must hand back the same frame, reset, and a
// different depth must hand back a different one.
func TestRunFrameReusePerDepth(t *testing.T) {
	in := &EVMInterpreter{VM: &VM{}}
	c := &Contract{}

	in.depth = 1
	mem, st, scope, pc := in.frame(c)
	mem.Resize(64)
	_ = mem.Set(0, 32, make([]byte, 32))
	st.PushSlot().SetUint64(7)
	*pc = 99

	mem2, st2, scope2, pc2 := in.frame(c)
	if mem2 != mem || st2 != st || scope2 != scope || pc2 != pc {
		t.Fatal("same depth must reuse the same frame")
	}
	if mem2.Len() != 0 || st2.Len() != 0 || *pc2 != 0 {
		t.Fatalf("frame not reset: mem=%d stack=%d pc=%d", mem2.Len(), st2.Len(), *pc2)
	}
	if scope2.Memory != mem2 || scope2.Stack != st2 || scope2.Contract != c {
		t.Fatal("scope not rebound to the frame")
	}

	in.depth = 2
	mem3, _, _, _ := in.frame(c)
	if mem3 == mem {
		t.Fatal("different depth must not share a frame")
	}
}

// TestRunFrameDropsOversizedMemory: a call that grew memory past the keep
// bound must not pin that buffer on the interpreter for later calls.
func TestRunFrameDropsOversizedMemory(t *testing.T) {
	in := &EVMInterpreter{VM: &VM{}}
	in.depth = 1
	mem, _, _, _ := in.frame(&Contract{})
	mem.Resize(runFrameMemoryKeep + 4096)
	if cap(mem.store) <= runFrameMemoryKeep {
		t.Skip("Resize did not grow the buffer past the keep bound")
	}
	mem2, _, _, _ := in.frame(&Contract{})
	if cap(mem2.store) > runFrameMemoryKeep {
		t.Fatalf("oversized buffer retained: cap=%d", cap(mem2.store))
	}
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

func BenchmarkRunFrameAcquire(b *testing.B) {
	in := &EVMInterpreter{VM: &VM{}}
	c := &Contract{}
	in.depth = 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem, _, _, _ := in.frame(c)
		mem.Resize(64)
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
