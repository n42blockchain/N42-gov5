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
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

var (
	bal7928Caller = types.HexToAddress("0x1000000000000000000000000000000000000001")
	bal7928Outer  = types.HexToAddress("0x2000000000000000000000000000000000000002")
	bal7928Inner  = types.HexToAddress("0x3000000000000000000000000000000000000003")
	bal7928Target = types.HexToAddress("0x4000000000000000000000000000000000000004")
)

// bal7928Run executes caller -> outer once under Amsterdam rules with the
// EIP-7928 recorder on, and returns the block access list.
func bal7928Run(t *testing.T, outer, inner []byte, setup func(*state.IntraBlockState)) bal.BlockAccessList {
	t.Helper()
	cfg := execHarnessChainConfig()
	cfg.PragueTime = big.NewInt(0)
	cfg.OsakaTime = big.NewInt(0)
	cfg.GlamsterdamTime = big.NewInt(0)
	evm, ibs := newExecHarnessEVM(t, cfg)
	ibs.CreateAccount(bal7928Caller, false)
	ibs.CreateAccount(bal7928Outer, false)
	ibs.SetCode(bal7928Outer, outer)
	if inner != nil {
		ibs.CreateAccount(bal7928Inner, false)
		ibs.SetCode(bal7928Inner, inner)
	}
	if setup != nil {
		setup(ibs)
	}
	ibs.AddAddressToAccessList(bal7928Outer)
	if err := ibs.FinalizeTx(evm.ChainRules(), state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}

	ibs.EnableBlockAccessList()
	ibs.SetBlockAccessIndex(1)
	if _, _, err := evm.Call(AccountRef(bal7928Caller), bal7928Outer, nil, 1_000_000, new(uint256.Int), false); err != nil {
		t.Fatalf("call: %v", err)
	}
	if err := ibs.FinalizeTx(evm.ChainRules(), state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	return ibs.BlockAccessList()
}

func bal7928Account(list bal.BlockAccessList, addr types.Address) *bal.AccountChanges {
	for i := range list {
		if list[i].Address == addr {
			return &list[i]
		}
	}
	return nil
}

// staticCallInner is outer's code: STATICCALL(gas, inner, 0, 0, 0, 0).
func staticCallInner() []byte {
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	code = append(code, bal7928Inner[:]...)
	return append(code, byte(GAS), byte(STATICCALL), byte(STOP))
}

// callTargetWithValue is CALL(gas, target, 1, 0, 0, 0, 0).
func callTargetWithValue() []byte {
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x01, 0x73}
	code = append(code, bal7928Target[:]...)
	return append(code, byte(GAS), byte(CALL), byte(STOP))
}

// A CALL with value in a static frame halts before the callee is touched, so
// the callee is not in the access list (go-ethereum rejects it in the gas
// function, before the EIP-7702 delegation lookup reads its code).
func TestAmsterdamStaticValueCallDoesNotAccessCallee(t *testing.T) {
	list := bal7928Run(t, staticCallInner(), callTargetWithValue(), nil)
	if bal7928Account(list, bal7928Target) != nil {
		t.Fatalf("callee of a static value CALL is in the access list: %+v", list)
	}
}

// An SSTORE in a static frame halts before the slot is read.
func TestAmsterdamStaticSStoreDoesNotReadSlot(t *testing.T) {
	sstore := []byte{0x60, 0x01, 0x60, 0x00, byte(SSTORE), byte(STOP)}
	list := bal7928Run(t, staticCallInner(), sstore, nil)
	if acc := bal7928Account(list, bal7928Inner); acc == nil || len(acc.StorageReads) != 0 || len(acc.StorageChanges) != 0 {
		t.Fatalf("static SSTORE touched storage: %+v", acc)
	}
}

// A SELFDESTRUCT in a static frame halts before the beneficiary is touched.
func TestAmsterdamStaticSelfdestructDoesNotAccessBeneficiary(t *testing.T) {
	selfdestruct := append([]byte{0x73}, bal7928Target[:]...)
	selfdestruct = append(selfdestruct, byte(SELFDESTRUCT))
	list := bal7928Run(t, staticCallInner(), selfdestruct, nil)
	if bal7928Account(list, bal7928Target) != nil {
		t.Fatalf("beneficiary of a static SELFDESTRUCT is in the access list: %+v", list)
	}
}

// The delegation target of a CALL that fails on the sender's balance has
// passed its gas check and is in the access list.
func TestAmsterdamDelegationTargetAccessedOnFailedCall(t *testing.T) {
	delegated := bal7928Target
	target := types.HexToAddress("0x5000000000000000000000000000000000000005")
	list := bal7928Run(t, callTargetWithValue(), nil, func(ibs *state.IntraBlockState) {
		ibs.CreateAccount(delegated, false)
		ibs.SetCode(delegated, append([]byte{0xef, 0x01, 0x00}, target[:]...))
	})
	if bal7928Account(list, delegated) == nil {
		t.Fatalf("delegated callee missing: %+v", list)
	}
	if bal7928Account(list, target) == nil {
		t.Fatalf("delegation target missing: %+v", list)
	}
	if acc := bal7928Account(list, bal7928Outer); acc == nil || len(acc.BalanceChanges) != 0 {
		t.Fatalf("outer frame: %+v", acc)
	}
}
