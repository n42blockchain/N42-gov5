// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package builder

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TestEqualTipOrderingIsRunsNotRotation records which of two shapes the block
// builder produces when every pending transaction carries the same gas price,
// which is what a flood generator produces and what the bench fleet runs.
//
// It matters for account locality. A block that ROTATES across senders --
// one transaction each, round robin -- touches a different sender account on
// every transaction, and a peer client measured full blocks drawn from a deep
// backlog costing twice as much per transaction as shallow ones for exactly
// that reason. A block built from RUNS keeps one sender hot for the length of
// its run.
//
// The answer follows from txsByPrice.Less returning false for equal tips, so
// heap.Fix leaves the replaced head in place -- but that is a reading of
// container/heap's semantics, and this test is the evidence for it.
func TestEqualTipOrderingIsRunsNotRotation(t *testing.T) {
	const senders, perSender = 4, 5
	pending := make(map[types.Address][]*transaction.Transaction, senders)
	to := types.HexToAddress("0x2000000000000000000000000000000000000001")
	for s := 0; s < senders; s++ {
		from := types.BytesToAddress([]byte{byte(0x10 + s)})
		txs := make([]*transaction.Transaction, perSender)
		for i := range txs {
			// Identical gas price across every sender: the flood's shape.
			txs[i] = transaction.NewTransaction(uint64(i), from, &to,
				uint256.NewInt(1), 21000, uint256.NewInt(1_000_000_007), nil)
		}
		pending[from] = txs
	}

	set := NewTxByPriceAndNonce(pending, uint256.NewInt(0))
	var order []types.Address
	for {
		tx := set.Peek()
		if tx == nil {
			break
		}
		from := tx.From()
		if from == nil {
			t.Fatal("pending transaction has no sender")
		}
		order = append(order, *from)
		set.Shift()
	}
	if len(order) != senders*perSender {
		t.Fatalf("drained %d transactions, want %d", len(order), senders*perSender)
	}

	switches := 0
	for i := 1; i < len(order); i++ {
		if order[i] != order[i-1] {
			switches++
		}
	}
	// Runs: one switch per sender boundary. Rotation: a switch almost every
	// transaction.
	if switches > senders {
		t.Fatalf("builder ROTATES across senders: %d sender switches in %d transactions "+
			"(runs would be <= %d). Account locality per block is then one cold sender "+
			"per transaction; see docs/QS_TPS_BENCHMARK.md", switches, len(order), senders)
	}
	t.Logf("runs confirmed: %d sender switches over %d transactions", switches, len(order))
}
