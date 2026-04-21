// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TestParallelTxRunner_MockEVM wires MockEVM through
// ExecuteBlockParallel + FinalizeBlock + Apply on a tiny synthetic
// block, verifying the full pipeline shape works end-to-end.
func TestParallelTxRunner_MockEVM(t *testing.T) {
	const numTxs = 5

	// Build N synthetic txs, each with a distinct destination.
	txs := make([]*transaction.Transaction, numTxs)
	senders := make([]types.Address, numTxs)
	for i := 0; i < numTxs; i++ {
		var to types.Address
		to[0] = byte(i + 1)
		txs[i] = transaction.NewTransaction(
			uint64(i),         // nonce
			types.Address{},   // from (filled by signer; mock doesn't care)
			&to,               // to (pointer)
			uint256.NewInt(0), // value
			21000,             // gasLimit
			uint256.NewInt(1), // gasPrice
			nil,               // data
		)
		senders[i] = types.Address{0xff, byte(i)}
	}

	block := &BlockContext{
		Coinbase:    types.Address{0xcb},
		BlockNumber: 1000,
		Time:        1700000000,
		GasLimit:    30_000_000,
		BaseFee:     uint256.NewInt(1),
		BlockHash:   func(uint64) types.Hash { return types.Hash{} },
	}

	outputs := make([]TxOutput, numTxs)
	executor := ParallelTxRunner(MockEVM{}, txs, senders, block, outputs)

	base := NewMapBaseReader(nil)
	_, mv, err := ExecuteBlockParallel(numTxs, 4, base, executor)
	if err != nil {
		t.Fatal(err)
	}

	bc, err := FinalizeBlock(mv, outputs, block.Coinbase)
	if err != nil {
		t.Fatal(err)
	}
	if bc.GasUsed != 21000*numTxs {
		t.Errorf("GasUsed=%d want %d", bc.GasUsed, 21000*numTxs)
	}
	// Each tx tipped 1 wei → coinbase delta = numTxs.
	if bc.CoinbaseDelta.Uint64() != uint64(numTxs) {
		t.Errorf("CoinbaseDelta=%d want %d", bc.CoinbaseDelta.Uint64(), numTxs)
	}

	target := NewMapApplyTarget()
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// MockEVM increments slot 0 of each tx.To. Final values should
	// each be 1 (each address touched once).
	for i := 0; i < numTxs; i++ {
		var to types.Address
		to[0] = byte(i + 1)
		slots := target.Storage[to]
		if slots == nil {
			t.Errorf("tx %d (to=%x): no storage", i, to)
			continue
		}
		v := slots[types.Hash{}]
		if len(v) != 1 || v[0] != 1 {
			t.Errorf("tx %d slot[0]=%v want [1]", i, v)
		}
	}

	// Coinbase balance should equal cumulative tip = numTxs.
	bal := target.Balance[block.Coinbase]
	if bal == nil || bal.Uint64() != uint64(numTxs) {
		t.Errorf("coinbase balance: %v want %d", bal, numTxs)
	}
}

func TestIsAbortRetry(t *testing.T) {
	if !IsAbortRetry(AbortRetry()) {
		t.Error("AbortRetry() should be IsAbortRetry")
	}
	if IsAbortRetry(nil) {
		t.Error("nil should not be IsAbortRetry")
	}
}

// TestParallelTxRunner_ContendedSameTo: all txs target the SAME
// address; their slot increments must serialize correctly via Block-STM.
func TestParallelTxRunner_ContendedSameTo(t *testing.T) {
	const numTxs = 10
	var sharedTo types.Address
	sharedTo[0] = 0xab

	txs := make([]*transaction.Transaction, numTxs)
	senders := make([]types.Address, numTxs)
	for i := 0; i < numTxs; i++ {
		txs[i] = transaction.NewTransaction(
			uint64(i), types.Address{}, &sharedTo, uint256.NewInt(0), 21000,
			uint256.NewInt(1), nil)
		senders[i] = types.Address{0xff, byte(i)}
	}

	block := &BlockContext{
		Coinbase:    types.Address{0xcb},
		BlockNumber: 1000,
		Time:        1700000000,
		GasLimit:    30_000_000,
		BaseFee:     uint256.NewInt(1),
		BlockHash:   func(uint64) types.Hash { return types.Hash{} },
	}

	outputs := make([]TxOutput, numTxs)
	executor := ParallelTxRunner(MockEVM{}, txs, senders, block, outputs)

	base := NewMapBaseReader(nil)
	_, mv, err := ExecuteBlockParallel(numTxs, 4, base, executor)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := FinalizeBlock(mv, outputs, block.Coinbase)
	if err != nil {
		t.Fatal(err)
	}
	target := NewMapApplyTarget()
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// All 10 increments to slot 0 → final value 10.
	v := target.Storage[sharedTo][types.Hash{}]
	if len(v) != 1 || v[0] != numTxs {
		t.Errorf("contended slot: got %v want [%d]", v, numTxs)
	}
}
