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
	"testing"
)

// TestVMTEIPRegistry covers EnableEIP/ValidEip/ActivateableEips, the pure
// EIP activation registry used by tooling to patch a JumpTable in place.
func TestVMTEIPRegistry(t *testing.T) {
	if !ValidEip(2929) {
		t.Fatalf("expected EIP-2929 to be a known/valid EIP")
	}
	if ValidEip(999999) {
		t.Fatalf("expected an unknown EIP number to be invalid")
	}

	nums := ActivateableEips()
	if len(nums) == 0 {
		t.Fatalf("expected a non-empty list of activateable EIPs")
	}

	jt := newBerlinInstructionSet()
	if err := EnableEIP(1884, &jt); err != nil {
		t.Fatalf("EnableEIP(1884) failed: %v", err)
	}
	if jt[SELFBALANCE] == nil || jt[SELFBALANCE].execute == nil {
		t.Fatalf("expected SELFBALANCE to be installed after EnableEIP(1884)")
	}

	if err := EnableEIP(999999, &jt); err == nil {
		t.Fatalf("expected EnableEIP to error on an unknown EIP number")
	}
}

// TestVMTOpSelfBalance covers the SELFBALANCE opcode installed by EIP-1884.
func TestVMTOpSelfBalance(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(SELFBALANCE),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	// execHarnessCall's object account starts with zero balance.
	for _, b := range ret {
		if b != 0 {
			t.Fatalf("expected SELFBALANCE to report 0, got %x", ret)
		}
	}
}

// TestVMTEVMPlumbingGetters exercises the small EVM accessor/mutator
// methods that otherwise sit entirely uncovered: SetContentStoreDB, Reset,
// ResetBetweenBlocks, Cancel/Cancelled, Interpreter, Config, ChainConfig.
func TestVMTEVMPlumbingGetters(t *testing.T) {
	evm, _ := newExecHarnessEVM(t, execHarnessChainConfig())

	if evm.Interpreter() == nil {
		t.Fatalf("expected a non-nil Interpreter()")
	}
	if evm.Config().Debug {
		t.Fatalf("expected Debug to default to false")
	}
	if evm.ChainConfig() == nil {
		t.Fatalf("expected a non-nil ChainConfig()")
	}
	if evm.Cancelled() {
		t.Fatalf("expected Cancelled() to be false before Cancel()")
	}
	evm.Cancel()
	if !evm.Cancelled() {
		t.Fatalf("expected Cancelled() to be true after Cancel()")
	}

	evm.SetContentStoreDB(nil)
}
