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
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func petersburgOnlyChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
	}
}

func constantinopleOnlyChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
	}
}

func istanbulOnlyChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
	}
}

// vmTSstore writes `value` to slot 0 against a fresh contract (so storage
// starts at zero) using a bare SSTORE and returns the leftover gas, to probe
// the per-fork dynamic gas function actually charged.
func vmTSstore(t *testing.T, cfg *params.ChainConfig, setup func(ibs *state.IntraBlockState, addr types.Address), value uint64) uint64 {
	t.Helper()
	evm, ibs := newExecHarnessEVM(t, cfg)

	caller := types.HexToAddress("0xcacacacacacacacacacacacacacacacacacacac")
	object := types.HexToAddress("0x0bec70beef0bec70beef0bec70beef0bec70bee")

	code := []byte{
		byte(PUSH8), 0, 0, 0, 0, 0, 0, 0, byte(value),
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}

	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000_000))
	ibs.CreateAccount(object, true)
	if setup != nil {
		setup(ibs, object)
	}
	ibs.SetCode(object, code)
	ibs.PrepareAccessList(caller, &object, nil, nil)

	_, leftOverGas, err := evm.Call(AccountRef(caller), object, nil, 1_000_000, uint256.NewInt(0), false)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return leftOverGas
}

// TestVMTGasSStoreLegacyPetersburg exercises the legacy (pre-Constantinople
// or Petersburg) 3-case SSTORE gas schedule in gasSStore.
func TestVMTGasSStoreLegacyPetersburg(t *testing.T) {
	cfg := petersburgOnlyChainConfig()

	// zero -> non-zero: SstoreSetGas (expensive).
	leftZeroToNonZero := vmTSstore(t, cfg, nil, 1)

	// non-zero -> non-zero: SstoreResetGas (cheaper than Set).
	leftNonZeroToNonZero := vmTSstore(t, cfg, func(ibs *state.IntraBlockState, addr types.Address) {
		key := types.Hash{}
		ibs.SetState(addr, &key, *uint256.NewInt(7))
	}, 2)

	if leftNonZeroToNonZero <= leftZeroToNonZero {
		t.Fatalf("expected non-zero->non-zero (Reset) to leave MORE gas than zero->non-zero (Set): got reset=%d set=%d", leftNonZeroToNonZero, leftZeroToNonZero)
	}
}

// TestVMTGasSStoreConstantinopleEIP1283 exercises the EIP-1283 net-metered
// branch of gasSStore (Constantinople active, Petersburg not).
func TestVMTGasSStoreConstantinopleEIP1283(t *testing.T) {
	cfg := constantinopleOnlyChainConfig()

	// zero -> zero (no-op, current==value): cheap NetSstoreNoopGas.
	leftNoop := vmTSstore(t, cfg, nil, 0)

	// zero -> non-zero (create slot, original==current==0): NetSstoreInitGas.
	leftInit := vmTSstore(t, cfg, nil, 1)

	if leftNoop <= leftInit {
		t.Fatalf("expected EIP-1283 no-op to be cheaper than slot-init: got noop_leftover=%d init_leftover=%d", leftNoop, leftInit)
	}
}

// TestVMTGasSStoreEIP2200Sentry exercises the gas-sentry failure branch of
// gasSStoreEIP2200: SSTORE must fail outright when remaining gas is at or
// below the EIP-2200 reentrancy sentry, regardless of the stored value.
func TestVMTGasSStoreEIP2200Sentry(t *testing.T) {
	cfg := istanbulOnlyChainConfig()
	evm, ibs := newExecHarnessEVM(t, cfg)

	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
	// Gas at or below params.SstoreSentryGasEIP2200 (2300) must fail the
	// reentrancy sentry check before any state change.
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 2300, nil)
	if err == nil {
		t.Fatalf("expected SSTORE to fail under the EIP-2200 gas sentry")
	}
}

// TestVMTGasSStoreEIP2200Noop exercises the no-op branch of
// gasSStoreEIP2200 (current == new value).
func TestVMTGasSStoreEIP2200Noop(t *testing.T) {
	cfg := istanbulOnlyChainConfig()
	evm, ibs := newExecHarnessEVM(t, cfg)

	// Storing 0 into an already-zero slot is a no-op under EIP-2200.
	code := []byte{
		byte(PUSH1), 0x00,
		byte(PUSH1), 0x00,
		byte(SSTORE),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
}
