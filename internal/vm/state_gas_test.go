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

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// The state budget draws on the reservoir first and borrows the rest from
// execution gas; a reverted frame refills every state charge and gives the
// borrowed execution gas back, a halted one refills the reservoir but burns
// its execution gas.
func TestStateGasBudgetChargeSpillExit(t *testing.T) {
	b := StateGasBudget{Reservoir: 100}
	exec := uint64(1000)
	if !b.Charge(&exec, 60) || b.Reservoir != 40 || exec != 1000 {
		t.Fatalf("charge within reservoir: reservoir %d exec %d", b.Reservoir, exec)
	}
	if !b.Charge(&exec, 100) || b.Reservoir != 0 || exec != 940 || b.Spilled != 60 || b.Used != 160 {
		t.Fatalf("spilling charge: %+v exec %d", b, exec)
	}
	if b.Charge(&exec, 941) {
		t.Fatal("charge beyond reservoir plus execution gas succeeded")
	}

	gas, out := b.Exit(exec, nil)
	if gas != 940 || out != b {
		t.Fatalf("success exit: gas %d budget %+v", gas, out)
	}
	gas, out = b.Exit(exec, ErrExecutionReverted)
	if gas != 1000 || out != (StateGasBudget{Reservoir: 100}) {
		t.Fatalf("revert exit: gas %d budget %+v, want 1000 and reservoir 100", gas, out)
	}
	gas, out = b.Exit(exec, ErrOutOfGas)
	if gas != 0 || out != (StateGasBudget{Reservoir: 100}) {
		t.Fatalf("halt exit: gas %d budget %+v, want 0 and reservoir 100", gas, out)
	}
}

// EIP-8037/8038: an SSTORE that creates a slot pays the cold access and the
// STORAGE_WRITE in execution gas and the 64-byte slot's state gas from the
// reservoir the transaction handed to the frame.
func TestAmsterdamSStoreChargesStateGasFromReservoir(t *testing.T) {
	cfg := execHarnessChainConfig()
	cfg.PragueTime = big.NewInt(0)
	cfg.OsakaTime = big.NewInt(0)
	cfg.GlamsterdamTime = big.NewInt(0)
	evm, ibs := newExecHarnessEVM(t, cfg)

	caller := types.HexToAddress("0x1000000000000000000000000000000000000001")
	contract := types.HexToAddress("0x2000000000000000000000000000000000000002")
	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(contract, false)
	ibs.SetCode(contract, []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}) // SSTORE(0, 1); STOP
	ibs.AddAddressToAccessList(contract)

	const execGas, reservoir = 100_000, 200_000
	evm.SetStateGasReservoir(reservoir)
	_, left, err := evm.Call(AccountRef(caller), contract, nil, execGas, new(uint256.Int), false)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	slotStateGas := params.StateBytesPerStorageSet * params.CostPerStateByteEIP8037
	wantLeft := uint64(execGas - 2*GasFastestStep - params.ColdSloadCostEIP2929 - params.StorageWriteEIP8038)
	if left != wantLeft {
		t.Fatalf("execution gas left = %d, want %d", left, wantLeft)
	}
	out := evm.StateGasLeftover()
	if out.Reservoir != reservoir-slotStateGas || out.Used != int64(slotStateGas) || out.Spilled != 0 {
		t.Fatalf("state budget after call = %+v, want reservoir %d used %d", out, reservoir-slotStateGas, slotStateGas)
	}
}
