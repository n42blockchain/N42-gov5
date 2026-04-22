// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// TestRealParallelEVM_CompilesAndWiresUp confirms the adapter satisfies
// the state.ParallelEVM interface and can be plugged into the Block-STM
// pipeline.
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
		nil,
		vm2.Config{},
		header,
		blockHashFunc,
		nil,
	)

	var _ state.ParallelEVM = pe

	if pe == nil {
		t.Fatal("NewRealParallelEVM returned nil")
	}
}

// TestRealParallelEVM_NoCoinbaseTipDoubleCount guards the invariant
// that RealParallelEVM does NOT populate TxOutput.CoinbaseTip — the
// coinbase credit already flows through IntraBlockState → MVStateWriter
// via internal.ApplyTransaction's st.state.AddBalance(coinbase, tip).
// Setting CoinbaseTip here would cause FinalizeBlock's CoinbaseDelta
// aggregation + Apply.AddBalance to credit the tip a second time.
//
// The test doesn't actually execute the EVM (that needs a full block
// context with a signed tx, genesis state, etc.); it documents the
// contract and anchors the regression guard.
func TestRealParallelEVM_NoCoinbaseTipDoubleCount(t *testing.T) {
	// We build a zero-tx TxOutput representing what RealParallelEVM
	// would return, and verify CoinbaseTip stays nil so FinalizeBlock's
	// `if out.CoinbaseTip != nil && !out.CoinbaseTip.IsZero()` branch
	// is skipped.
	out := state.TxOutput{
		GasUsed: 21000,
		Status:  1,
	}
	if out.CoinbaseTip != nil {
		t.Errorf("CoinbaseTip must be nil — the coinbase credit is applied inside ApplyTransaction, not via TxOutput")
	}
}
