// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package vm

import (
	"reflect"
	"testing"
	"unsafe"
)

// allInstructionSets is every table NewEVMInterpreter can select. A new fork
// added to that switch without a line here would go untested, so the count is
// asserted below.
func allInstructionSets() map[string]*JumpTable {
	return map[string]*JumpTable{
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
		"pectra":           &pectraInstructionSet,
		"osaka":            &osakaInstructionSet,
		"osakaEOF":         &osakaEOFInstructionSet,
		"fusaka":           &fusakaInstructionSet,
		"fusakaEOF":        &fusakaEOFInstructionSet,
		"glamsterdam":      &glamsterdamInstructionSet,
		"glamsterdamEOF":   &glamsterdamEOFInstructionSet,
	}
}

// TestOpMetaMatchesJumpTable is the correctness gate for the flat dispatch
// table: for every fork and every one of the 256 opcodes, the values the
// interpreter loop now reads must be the values it used to read out of
// *operation. A drift here is a consensus bug, not a performance one.
func TestOpMetaMatchesJumpTable(t *testing.T) {
	for name, jt := range allInstructionSets() {
		t.Run(name, func(t *testing.T) {
			meta := opMetaFor(jt)
			for op := 0; op < 256; op++ {
				want := jt[op]
				if want == nil {
					t.Fatalf("op 0x%02x: jump table entry is nil", op)
				}
				got := &meta[op]
				if OpCode(op) >= fusedPush1Jump && OpCode(op) <= fusedPush2Jumpi {
					// Synthetic static-jump opcodes (fuse.go): PUSH + JUMP/JUMPI
					// folded into one record. The jump table entry is undefined.
					jumpGas := jt[JUMP].constantGas
					if op == int(fusedPush1Jumpi) || op == int(fusedPush2Jumpi) {
						jumpGas = jt[JUMPI].constantGas
						if got.numPop != 1 {
							t.Errorf("op 0x%02x numPop: got %d want 1", op, got.numPop)
						}
					} else if got.numPop != 0 {
						t.Errorf("op 0x%02x numPop: got %d want 0", op, got.numPop)
					}
					if got.constantGas != jt[PUSH1].constantGas+jumpGas || got.maxStack != jt[PUSH1].maxStack || got.dynamicGas != nil || got.memorySize != nil {
						t.Errorf("op 0x%02x: fused record %+v does not match PUSH+JUMP", op, *got)
					}
					continue
				}
				if got.constantGas != want.constantGas {
					t.Errorf("op 0x%02x constantGas: got %d want %d", op, got.constantGas, want.constantGas)
				}
				if got.numPop != want.numPop {
					t.Errorf("op 0x%02x numPop: got %d want %d", op, got.numPop, want.numPop)
				}
				if got.maxStack != want.maxStack {
					t.Errorf("op 0x%02x maxStack: got %d want %d", op, got.maxStack, want.maxStack)
				}
				if !sameFunc(got.execute, want.execute) {
					t.Errorf("op 0x%02x execute: flat table points at a different function", op)
				}
				if !sameFunc(got.dynamicGas, want.dynamicGas) {
					t.Errorf("op 0x%02x dynamicGas: flat table points at a different function", op)
				}
				if !sameFunc(got.memorySize, want.memorySize) {
					t.Errorf("op 0x%02x memorySize: flat table points at a different function", op)
				}
			}
		})
	}
}

// TestOpMetaCoversEveryFork keeps allInstructionSets honest: NewEVMInterpreter
// selects one table per fork, so the set under test must not fall behind.
func TestOpMetaCoversEveryFork(t *testing.T) {
	if got, want := len(allInstructionSets()), 19; got != want {
		t.Fatalf("instruction set count changed: got %d want %d — add the new fork's table to allInstructionSets", got, want)
	}
}

// TestOpMetaForIsMemoized proves a per-transaction EVM does not rebuild the
// 16 KiB table: the same JumpTable must hand back the same pointer.
func TestOpMetaForIsMemoized(t *testing.T) {
	first := opMetaFor(&cancunInstructionSet)
	second := opMetaFor(&cancunInstructionSet)
	if first != second {
		t.Fatalf("opMetaFor rebuilt the table for the same JumpTable")
	}
}

// TestOpMetaFitsOneCacheLine pins the padding: the whole point of the flat
// table is that an opcode's fields never straddle two 64-byte lines.
func TestOpMetaFitsOneCacheLine(t *testing.T) {
	if got := unsafe.Sizeof(opMeta{}); got != 64 {
		t.Fatalf("opMeta is %d bytes, want 64 — adjust the padding field", got)
	}
}

// sameFunc compares two func values by code pointer. Function values are not
// comparable with ==, and these are all plain top-level or closure-free
// functions, so the code pointer identifies them.
func sameFunc(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.IsNil() != bv.IsNil() {
		return false
	}
	if av.IsNil() {
		return true
	}
	return av.Pointer() == bv.Pointer()
}
