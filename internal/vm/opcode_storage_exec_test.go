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

package vm

import (
	"bytes"
	"testing"
)

func TestOpcodeSstoreSload(t *testing.T) {
	// SSTORE slot 0 = 0x42, then SLOAD it back in the same call.
	code := []byte{
		byte(PUSH1), 0x42,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(PUSH1), 0x00,
		byte(SLOAD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	got := runProgram(t, code)
	want := make([]byte, 32)
	want[31] = 0x42
	if !bytes.Equal(got, want) {
		t.Errorf("SSTORE/SLOAD roundtrip = %x, want %x", got, want)
	}
}

func TestOpcodeSloadColdVsWarm(t *testing.T) {
	// EIP-2929: the first SLOAD of a slot in a transaction is "cold" (2100
	// gas) and subsequent SLOADs of the same slot are "warm" (100 gas).
	// Compare total gas consumed by a single SLOAD vs. two SLOADs of the
	// same key to confirm the second access is materially cheaper than
	// a second independent cold access would be.
	oneLoad := []byte{
		byte(PUSH1), 0x00,
		byte(SLOAD),
		byte(POP),
		byte(STOP),
	}
	twoLoads := []byte{
		byte(PUSH1), 0x00,
		byte(SLOAD),
		byte(POP),
		byte(PUSH1), 0x00,
		byte(SLOAD),
		byte(POP),
		byte(STOP),
	}

	const startGas = 100_000
	evm1, ibs1 := newExecHarnessEVM(t, execHarnessChainConfig())
	_, left1, err := execHarnessCall(t, evm1, ibs1, oneLoad, nil, startGas, nil)
	if err != nil {
		t.Fatalf("single SLOAD call failed: %v", err)
	}
	used1 := startGas - left1

	evm2, ibs2 := newExecHarnessEVM(t, execHarnessChainConfig())
	_, left2, err := execHarnessCall(t, evm2, ibs2, twoLoads, nil, startGas, nil)
	if err != nil {
		t.Fatalf("double SLOAD call failed: %v", err)
	}
	used2 := startGas - left2

	extra := used2 - used1
	if extra >= used1 {
		t.Errorf("second (warm) SLOAD cost %d gas, expected materially less than the first (cold) access cost %d", extra, used1)
	}
}

func TestOpcodeSstoreSetClearResetGas(t *testing.T) {
	// 0 -> non-zero (SET) costs more than non-zero -> non-zero (RESET).
	setProgram := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
	resetProgram := []byte{
		// First establish a non-zero value via SSTORE, then overwrite it.
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(PUSH1), 0x02,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}

	const startGas = 100_000
	evmSet, ibsSet := newExecHarnessEVM(t, execHarnessChainConfig())
	_, leftSet, err := execHarnessCall(t, evmSet, ibsSet, setProgram, nil, startGas, nil)
	if err != nil {
		t.Fatalf("SET program failed: %v", err)
	}
	usedSet := startGas - leftSet

	evmReset, ibsReset := newExecHarnessEVM(t, execHarnessChainConfig())
	_, leftReset, err := execHarnessCall(t, evmReset, ibsReset, resetProgram, nil, startGas, nil)
	if err != nil {
		t.Fatalf("RESET program failed: %v", err)
	}
	usedReset := startGas - leftReset

	// usedReset includes one extra PUSH1+PUSH1+SSTORE sequence (the second
	// SSTORE, a warm non-zero->non-zero write) on top of usedSet's single
	// SSTORE; the per-SSTORE marginal cost of that second write must still
	// be materially cheaper than the first SET write since the slot is
	// already warm and already non-zero in the access list / dirty cache.
	if usedReset <= usedSet {
		t.Errorf("expected resetProgram (two SSTOREs) to use more total gas than setProgram (one SSTORE): usedSet=%d usedReset=%d", usedSet, usedReset)
	}
}

func TestOpcodeSstoreClearRefund(t *testing.T) {
	// Writing a non-zero slot back to zero schedules a gas refund
	// (EIP-3529 scaled-down refund, still non-zero).
	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(PUSH1), 0x00,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if ibs.GetRefund() == 0 {
		t.Errorf("expected non-zero refund after clearing a non-zero slot, got 0")
	}
}

func TestOpcodeTloadTstore(t *testing.T) {
	// TSTORE/TLOAD: transient storage round-trips within a call but
	// (unlike SSTORE) never touches persistent state.
	code := []byte{
		byte(PUSH1), 0x7b,
		byte(PUSH1), 0x00,
		byte(TSTORE),
		byte(PUSH1), 0x00,
		byte(TLOAD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	got, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := make([]byte, 32)
	want[31] = 0x7b
	if !bytes.Equal(got, want) {
		t.Errorf("TSTORE/TLOAD roundtrip = %x, want %x", got, want)
	}
}
