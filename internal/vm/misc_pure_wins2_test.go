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

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// TestVMTExistPure covers the existPure gas-helper, both for an
// *state.IntraBlockState (fast path) and the generic StateDB fallback.
func TestVMTExistPure(t *testing.T) {
	_, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	addr := types.HexToAddress("0x00000000000000000000000000000000f00001")
	if existPure(ibs, addr) {
		t.Fatalf("expected existPure to report false for an untouched address")
	}
	ibs.CreateAccount(addr, true)
	if !existPure(ibs, addr) {
		t.Fatalf("expected existPure to report true after CreateAccount")
	}
}

// TestVMTEmitTransferLog covers the EIP-7708 transfer-log emission helper.
func TestVMTEmitTransferLog(t *testing.T) {
	_, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	from := types.HexToAddress("0x00000000000000000000000000000000f00002")
	to := types.HexToAddress("0x00000000000000000000000000000000f00003")

	EmitTransferLog(ibs, from, to, uint256.NewInt(42))

	logs := ibs.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Address != TransferLogAddress {
		t.Fatalf("expected log address %s, got %s", TransferLogAddress, logs[0].Address)
	}
	if len(logs[0].Topics) != 3 {
		t.Fatalf("expected 3 topics (signature, from, to), got %d", len(logs[0].Topics))
	}
}

// TestVMTEVMResetBetweenBlocks covers the ResetBetweenBlocks plumbing
// method, which rebuilds the interpreter for a fresh block context.
func TestVMTEVMResetBetweenBlocks(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	oldInterp := evm.Interpreter()

	evm.ResetBetweenBlocks(evm.Context(), evm.TxContext(), ibs, evm.Config(), evm.ChainRules())

	if evm.Interpreter() == nil {
		t.Fatalf("expected a non-nil Interpreter after ResetBetweenBlocks")
	}
	if evm.Interpreter() == oldInterp {
		t.Fatalf("expected ResetBetweenBlocks to rebuild the interpreter")
	}
	if evm.Cancelled() {
		t.Fatalf("expected Cancelled() to be false after ResetBetweenBlocks")
	}

	// The reset EVM must still execute code correctly.
	code := []byte{
		byte(PUSH1), 0x07,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed after ResetBetweenBlocks: %v", err)
	}
	if new(uint256.Int).SetBytes(ret).Uint64() != 7 {
		t.Fatalf("unexpected result after ResetBetweenBlocks: %x", ret)
	}
}
