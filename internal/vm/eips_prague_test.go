// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Prague EIPs.

package vm

import (
	"testing"
)

// isCLZEnabled checks if the CLZ opcode is properly enabled in an instruction set
// (has the expected gas cost and stack requirements).
func isCLZEnabled(jt *JumpTable) bool {
	return jt[CLZ] != nil &&
		jt[CLZ].constantGas == GasFastStep &&
		jt[CLZ].numPop == 1 &&
		jt[CLZ].numPush == 1
}

// EIP-7939: CLZ Tests (Moved to Fusaka)

func TestEIP7939NotInPrague(t *testing.T) {
	jt := newPragueInstructionSet()
	if isCLZEnabled(&jt) {
		t.Error("CLZ (0x1E) should NOT be enabled in Prague - it's a Fusaka feature")
	}
}

func TestCLZOpcode(t *testing.T) {
	if CLZ != 0x1e {
		t.Errorf("CLZ opcode = 0x%x, want 0x1e", byte(CLZ))
	}
}

func TestCLZString(t *testing.T) {
	if CLZ.String() != "CLZ" {
		t.Errorf("CLZ string = %s, want CLZ", CLZ.String())
	}
}

// Prague Instruction Set Completeness

func TestPragueInstructionSet(t *testing.T) {
	jt := newPragueInstructionSet()

	cancunOpcodes := []OpCode{TLOAD, TSTORE, MCOPY, BLOBHASH, BLOBBASEFEE}
	for _, op := range cancunOpcodes {
		if jt[op] == nil || (jt[op].constantGas == 0 && jt[op].dynamicGas == nil) {
			t.Errorf("Opcode %s should be enabled in Prague", op.String())
		}
	}

	if isCLZEnabled(&jt) {
		t.Error("CLZ should NOT be enabled in Prague (it's a Fusaka feature)")
	}
}

// Prague EIP Activators

func TestPragueEIPActivators(t *testing.T) {
	if _, ok := activators[7939]; !ok {
		t.Error("EIP-7939 activator should be registered")
	}
}

// Backward Compatibility

func TestCancunDoesNotHaveCLZ(t *testing.T) {
	jt := newCancunInstructionSet()
	if isCLZEnabled(&jt) {
		t.Error("CLZ should NOT be enabled in Cancun instruction set")
	}
}
