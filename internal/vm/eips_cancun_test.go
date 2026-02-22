// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Cancun EIPs.

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestEIP1153Enabled(t *testing.T) {
	jt := newCancunInstructionSet()

	if jt[TLOAD] == nil || jt[TLOAD].execute == nil {
		t.Error("TLOAD should be enabled in Cancun")
	}
	if jt[TSTORE] == nil || jt[TSTORE].execute == nil {
		t.Error("TSTORE should be enabled in Cancun")
	}
}

func TestEIP1153GasCost(t *testing.T) {
	jt := newCancunInstructionSet()
	expectedGas := params.WarmStorageReadCostEIP2929

	if jt[TLOAD].constantGas != expectedGas {
		t.Errorf("TLOAD gas = %d, want %d", jt[TLOAD].constantGas, expectedGas)
	}
	if jt[TSTORE].constantGas != expectedGas {
		t.Errorf("TSTORE gas = %d, want %d", jt[TSTORE].constantGas, expectedGas)
	}
}

func TestEIP5656Enabled(t *testing.T) {
	jt := newCancunInstructionSet()

	if jt[MCOPY] == nil || jt[MCOPY].execute == nil {
		t.Error("MCOPY should be enabled in Cancun")
	}
	if jt[MCOPY].numPop != 3 {
		t.Errorf("MCOPY numPop = %d, want 3", jt[MCOPY].numPop)
	}
	if jt[MCOPY].numPush != 0 {
		t.Errorf("MCOPY numPush = %d, want 0", jt[MCOPY].numPush)
	}
}

func TestEIP4844Enabled(t *testing.T) {
	jt := newCancunInstructionSet()

	if jt[BLOBHASH] == nil || jt[BLOBHASH].execute == nil {
		t.Error("BLOBHASH should be enabled in Cancun")
	}
}

func TestEIP7516Enabled(t *testing.T) {
	jt := newCancunInstructionSet()

	if jt[BLOBBASEFEE] == nil || jt[BLOBBASEFEE].execute == nil {
		t.Error("BLOBBASEFEE should be enabled in Cancun")
	}
	if jt[BLOBBASEFEE].constantGas != GasQuickStep {
		t.Errorf("BLOBBASEFEE gas = %d, want %d", jt[BLOBBASEFEE].constantGas, GasQuickStep)
	}
}

func TestCancunInstructionSet(t *testing.T) {
	jt := newCancunInstructionSet()

	cancunOpcodes := []OpCode{
		TLOAD,       // EIP-1153
		TSTORE,      // EIP-1153
		MCOPY,       // EIP-5656
		BLOBHASH,    // EIP-4844
		BLOBBASEFEE, // EIP-7516
	}

	for _, op := range cancunOpcodes {
		if jt[op] == nil {
			t.Errorf("Opcode %s should be enabled in Cancun", op.String())
		}
	}
}

func TestCancunHasAllShanghaiOpcodes(t *testing.T) {
	shanghaiJt := newShanghaiInstructionSet()
	cancunJt := newCancunInstructionSet()

	shanghaiOpcodes := []OpCode{PUSH0}

	for _, op := range shanghaiOpcodes {
		if shanghaiJt[op] == nil || shanghaiJt[op].execute == nil {
			t.Errorf("Opcode %s should be in Shanghai", op.String())
		}
		if cancunJt[op] == nil || cancunJt[op].execute == nil {
			t.Errorf("Opcode %s should also be in Cancun", op.String())
		}
	}
}

func TestCancunEIPActivators(t *testing.T) {
	eips := []int{1153, 5656, 4844, 7516, 6780}
	for _, eip := range eips {
		if _, ok := activators[eip]; !ok {
			t.Errorf("EIP-%d activator should be registered", eip)
		}
	}
}

func TestCancunOpcodeStrings(t *testing.T) {
	opcodes := map[OpCode]string{
		TLOAD:       "TLOAD",
		TSTORE:      "TSTORE",
		MCOPY:       "MCOPY",
		BLOBHASH:    "BLOBHASH",
		BLOBBASEFEE: "BLOBBASEFEE",
	}

	for op, expected := range opcodes {
		if op.String() != expected {
			t.Errorf("Opcode 0x%x: got %s, want %s", byte(op), op.String(), expected)
		}
	}
}
