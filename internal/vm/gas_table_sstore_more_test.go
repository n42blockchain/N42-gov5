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
	"github.com/n42blockchain/N42/modules/state"
)

// TestVMTGasSStoreLegacyDelete exercises the legacy non-zero -> zero
// (DELETE, with refund) branch of gasSStore.
func TestVMTGasSStoreLegacyDelete(t *testing.T) {
	cfg := petersburgOnlyChainConfig()
	left := vmTSstore(t, cfg, func(ibs *state.IntraBlockState, addr types.Address) {
		key := types.Hash{}
		ibs.SetState(addr, &key, *uint256.NewInt(7))
	}, 0)
	if left == 0 {
		t.Fatalf("expected some leftover gas")
	}
}

// TestVMTGasSStoreEIP2200DirtySlot exercises the "dirty slot" branch of
// gasSStoreEIP2200: SSTORE twice in the same call so current != original
// on the second write.
func TestVMTGasSStoreEIP2200DirtySlot(t *testing.T) {
	cfg := istanbulOnlyChainConfig()
	evm, ibs := newExecHarnessEVM(t, cfg)

	// SSTORE slot0=1 (init), then SSTORE slot0=2 (dirty: current=1 != original=0,
	// new value=2 != current).
	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(PUSH1), 0x02,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

// TestVMTOpDup1AndSwap1 covers the DUP1/SWAP1 instantiations of
// makeDup/makeSwap via real bytecode execution (as opposed to the DUP16/
// SWAP16 instantiations covered elsewhere).
func TestVMTOpDup1AndSwap1(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(PUSH1), 0x05,
		byte(DUP1),  // stack: 5,5
		byte(PUSH1), 0x09,
		byte(SWAP1), // stack: 5,9,5 -> bottom to top after swap1 on top two
		byte(POP),
		byte(POP),
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
	if new(uint256.Int).SetBytes(ret).Uint64() != 5 {
		t.Fatalf("unexpected result: %x", ret)
	}
}
