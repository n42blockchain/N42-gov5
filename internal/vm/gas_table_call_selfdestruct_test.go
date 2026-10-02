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

// TestVMTGasSelfdestructFrontierNoSurcharge exercises the pre-TangerineWhistle
// branch of gasSelfdestruct (no new-account surcharge at all).
func TestVMTGasSelfdestructFrontierNoSurcharge(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, frontierOnlyChainConfig())
	beneficiary := types.HexToAddress("0x000000000000000000000000000000000beef9")
	code := selfdestructToCode(beneficiary)
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

// TestVMTGasSelfdestructTangerineWhistleNewAccountSurcharge exercises the
// IsTangerineWhistle branch of gasSelfdestruct, including the
// CreateBySelfdestructGas surcharge for sweeping a nonzero balance into a
// brand-new (empty) beneficiary account.
func TestVMTGasSelfdestructTangerineWhistleNewAccountSurcharge(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, istanbulOnlyChainConfig())
	beneficiary := types.HexToAddress("0x000000000000000000000000000000000beefa") // never touched -> new/empty

	caller := types.HexToAddress("0x00000000000000000000000000000000ca11eb")
	object := types.HexToAddress("0x0bec70beef0bec70beef0bec70beef0bec70bee")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.CreateAccount(object, true)
	ibs.SetCode(object, selfdestructToCode(beneficiary))
	ibs.AddBalance(object, uint256.NewInt(100))
	ibs.PrepareAccessList(caller, &object, nil, nil)

	_, leftOverGas, err := evm.Call(AccountRef(caller), object, nil, 1_000_000, uint256.NewInt(0), false)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if got := ibs.GetBalance(beneficiary).Uint64(); got != 100 {
		t.Fatalf("expected beneficiary to receive 100, got %d", got)
	}
	// Sanity: the surcharge is substantial (25000 gas under EIP150), so the
	// leftover must be comfortably less than the full budget.
	if leftOverGas == 0 || leftOverGas >= 1_000_000 {
		t.Fatalf("unexpected leftover gas %d", leftOverGas)
	}
}

// TestVMTGasCallNewAccountSurcharge exercises the Spurious-Dragon-era
// new-account surcharge branch of gasCall: a value-transferring CALL to an
// empty (nonexistent) address must charge CallNewAccountGas in addition to
// CallValueTransferGas.
func TestVMTGasCallNewAccountSurcharge(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	target := types.HexToAddress("0x000000000000000000000000000000000beefb") // never touched -> empty

	targetWord := make([]byte, 32)
	copy(targetWord[12:], target.Bytes())

	// CALL(gas, target, value=1, in=0,0, out=0,0)
	code := []byte{
		byte(PUSH1), 0x00, // retSize
		byte(PUSH1), 0x00, // retOffset
		byte(PUSH1), 0x00, // argsSize
		byte(PUSH1), 0x00, // argsOffset
		byte(PUSH1), 0x01, // value = 1
		byte(PUSH32),
	}
	code = append(code, targetWord...)
	code = append(code,
		byte(PUSH3), 0x0f, 0x42, 0x40, // gas = 1_000_000
		byte(CALL),
		byte(POP),
		byte(STOP),
	)

	// execHarnessCall's "object" (the contract whose code runs) starts with
	// zero balance; fund it via the value passed into the top-level call so
	// the inner CALL's value transfer can actually succeed.
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 2_000_000, uint256.NewInt(5))
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if got := ibs.GetBalance(target).Uint64(); got != 1 {
		t.Fatalf("expected target to receive 1 wei, got %d", got)
	}
}

// TestVMTGasCallCodeValueTransfer exercises the value-transfer branch of
// gasCallCode (CallValueTransferGas), distinct from gasCall's new-account
// surcharge since CALLCODE never touches a separate recipient's existence.
func TestVMTGasCallCodeValueTransfer(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())

	callee := types.HexToAddress("0x00000000000000000000000000000000c0deef")
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{byte(STOP)})

	calleeWord := make([]byte, 32)
	copy(calleeWord[12:], callee.Bytes())

	code := []byte{
		byte(PUSH1), 0x00, // retSize
		byte(PUSH1), 0x00, // retOffset
		byte(PUSH1), 0x00, // argsSize
		byte(PUSH1), 0x00, // argsOffset
		byte(PUSH1), 0x01, // value = 1 (CALLCODE keeps the value with the caller's balance accounting)
		byte(PUSH32),
	}
	code = append(code, calleeWord...)
	code = append(code,
		byte(PUSH3), 0x0f, 0x42, 0x40, // gas
		byte(CALLCODE),
		byte(POP),
		byte(STOP),
	)

	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 2_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
}
