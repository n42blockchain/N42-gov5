// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// TestRealParallelEVM_CompilesAndWiresUp confirms the adapter satisfies
// the state.ParallelEVM interface and can be plugged into the Block-STM
// pipeline. Does NOT execute real EVM (that needs a full block context
// with signer, chain rules, genesis state, etc. — integration test
// belongs in ethexec).
func TestRealParallelEVM_CompilesAndWiresUp(t *testing.T) {
	var bn uint256.Int
	bn.SetUint64(1)
	header := &block.Header{
		Number:   &bn,
		GasLimit: 30_000_000,
		Time:     1700000000,
	}
	copy(header.Coinbase[:], []byte{0xcc})

	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }

	pe := NewRealParallelEVM(
		params.EthereumMainnetChainConfig,
		nil, // no engine — mock-like wiring test
		vm2.Config{},
		header,
		blockHashFunc,
		nil, // use header.Coinbase as author
	)

	// Interface assertion: should satisfy state.ParallelEVM.
	var _ state.ParallelEVM = pe

	// Constructor smoke: does it actually build one?
	if pe == nil {
		t.Fatal("NewRealParallelEVM returned nil")
	}
}

// TestComputeCoinbaseTip_PreLondon verifies the tip formula for legacy
// gas-price transactions.
func TestComputeCoinbaseTip_PreLondon(t *testing.T) {
	to := types.Address{}
	tx := transaction.NewTransaction(0, types.Address{}, &to,
		uint256.NewInt(0), 21000, uint256.NewInt(100), nil)
	var bn uint256.Int
	bn.SetUint64(1)
	header := &block.Header{Number: &bn} // no BaseFee → pre-London

	tip := computeCoinbaseTip(tx, header, 21000)
	want := uint256.NewInt(100 * 21000)
	if tip.Cmp(want) != 0 {
		t.Errorf("pre-London tip: got %s want %s", tip.String(), want.String())
	}
}

// TestComputeCoinbaseTip_London verifies the EIP-1559 tip formula:
// tip = (min(feeCap, baseFee+tipCap) - baseFee) × gasUsed.
func TestComputeCoinbaseTip_London(t *testing.T) {
	to := types.Address{}
	// Build a dynamic-fee tx by hand — the factory NewTransaction is
	// legacy; for this unit test we skip the dynamic path and just
	// verify the pre-London branch covers a no-baseFee header (the
	// primary branch our production path will hit for Frontier era).
	tx := transaction.NewTransaction(0, types.Address{}, &to,
		uint256.NewInt(0), 21000, uint256.NewInt(50), nil)
	var bn uint256.Int
	bn.SetUint64(13_000_000)
	header := &block.Header{
		Number:   &bn,
		BaseFee:  uint256.NewInt(10),
		GasLimit: 30_000_000,
	}

	// Legacy tx under London: gasPrice IS both feeCap and tip.
	// Effective tip/gas = gasPrice - baseFee = 50 - 10 = 40.
	// Total = 40 × 21000 = 840000.
	tip := computeCoinbaseTip(tx, header, 21000)
	// Legacy tx: GasTipCap() == GasFeeCap() == GasPrice() = 50.
	// priority = min(50, 10+50) = 50. perGas = 50-10 = 40. tip = 40*21000.
	want := uint256.NewInt(40 * 21000)
	if tip.Cmp(want) != 0 {
		t.Errorf("London tip: got %s want %s", tip.String(), want.String())
	}
}

// TestComputeCoinbaseTip_ZeroGas returns zero (defensive).
func TestComputeCoinbaseTip_ZeroGas(t *testing.T) {
	to := types.Address{}
	tx := transaction.NewTransaction(0, types.Address{}, &to,
		uint256.NewInt(0), 21000, uint256.NewInt(100), nil)
	header := &block.Header{}
	if !computeCoinbaseTip(tx, header, 0).IsZero() {
		t.Error("zero gas should yield zero tip")
	}
}
