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

func pragueOnlyChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		MergeNetsplitBlock:    big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
		PragueTime:            big.NewInt(0),
	}
}

// TestVMTResolveDelegationAndCall exercises EIP-7702 code resolution: a CALL
// to an EOA-like account whose code is a delegation designator must run the
// delegated implementation's code instead (evm.resolveCode's Prague branch),
// and ResolveDelegation/resolveCodeHash must report the delegated target.
func TestVMTResolveDelegationAndCall(t *testing.T) {
	cfg := pragueOnlyChainConfig()
	evm, ibs := newExecHarnessEVM(t, cfg)

	impl := types.HexToAddress("0x00000000000000000000000000000000de1e9a")
	delegator := types.HexToAddress("0x00000000000000000000000000000000de1e90")

	ibs.CreateAccount(impl, true)
	ibs.SetCode(impl, []byte{
		byte(PUSH1), 0x2a,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	})

	ibs.CreateAccount(delegator, true)
	ibs.SetCode(delegator, AddressToDelegation(impl))

	caller := types.HexToAddress("0x00000000000000000000000000000000ca11ee")
	ibs.CreateAccount(caller, true)
	ibs.PrepareAccessList(caller, &delegator, nil, nil)

	ret, _, err := evm.Call(AccountRef(caller), delegator, nil, 1_000_000, uint256.NewInt(0), false)
	if err != nil {
		t.Fatalf("call to delegated account failed: %v", err)
	}
	if new(uint256.Int).SetBytes(ret).Uint64() != 42 {
		t.Fatalf("expected the delegated implementation's return value 42, got %x", ret)
	}

	if got := ResolveDelegation(evm, delegator); got != impl {
		t.Fatalf("ResolveDelegation(delegator) = %s, want %s", got, impl)
	}
	if got := ResolveDelegation(evm, impl); got != impl {
		t.Fatalf("ResolveDelegation(non-delegated) should return the address itself, got %s", got)
	}

	if got := evm.resolveCodeHash(delegator); got != ibs.GetCodeHash(impl) {
		t.Fatalf("resolveCodeHash(delegator) should resolve to the implementation's code hash")
	}
}
