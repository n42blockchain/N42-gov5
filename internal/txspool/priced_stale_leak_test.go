// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression tests for the priced-list stale-entry leak: transactions that
// leave the pool because a block (usually another proposer's) included them
// must be counted as stale in the priced list, or the heaps keep a pointer to
// every gossiped transaction forever once SetBaseFee stops reheaping on an
// unchanged base fee.

package txspool

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func pricedLen(pool *TxsPool) int {
	return len(pool.priced.urgent.list) + len(pool.priced.floating.list)
}

// TestPricedListBoundedWhenPendingIncluded drives the common steady state: a
// remote transaction is promoted to pending, a block includes it (the account
// nonce advances), and the reorg's demoteUnexecutables drops it. The base fee
// never changes, so SetBaseFee does not reheap.
func TestPricedListBoundedWhenPendingIncluded(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	pool.priced.SetBaseFee(uint256.NewInt(7))
	addr := types.Address{0x51}
	state.balances[addr] = uint256.NewInt(1_000_000_000_000)

	const rounds = 2000
	for n := uint64(0); n < rounds; n++ {
		state.setNonce(addr, n)
		tx := newTestTxWithFrom(n, 10, addr)
		if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
			t.Fatalf("enqueueTx %d: %v", n, err)
		}
		if got := pool.promoteExecutables([]types.Address{addr}); len(got) != 1 {
			t.Fatalf("round %d: promoted %d, want 1", n, len(got))
		}
		// Another proposer's block includes the transaction.
		state.setNonce(addr, n+1)
		pool.demoteUnexecutables()
		pool.priced.SetBaseFee(uint256.NewInt(7)) // unchanged: no reheap
		if pool.all.Count() != 0 {
			t.Fatalf("round %d: lookup holds %d txs, want 0", n, pool.all.Count())
		}
	}
	if got := pricedLen(pool); got > 16 {
		t.Fatalf("priced list retains %d entries after %d included txs; want a small bound", got, rounds)
	}
}

// TestPricedListBoundedWhenQueuedIncluded covers a transaction still in the
// future queue when a block includes it: promoteExecutables forwards it out.
func TestPricedListBoundedWhenQueuedIncluded(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	pool.priced.SetBaseFee(uint256.NewInt(7))
	addr := types.Address{0x52}
	state.balances[addr] = uint256.NewInt(1_000_000_000_000)

	const rounds = 2000
	for n := uint64(0); n < rounds; n++ {
		tx := newTestTxWithFrom(n, 10, addr)
		if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
			t.Fatalf("enqueueTx %d: %v", n, err)
		}
		// Included before it was ever promoted.
		state.setNonce(addr, n+1)
		pool.promoteExecutables([]types.Address{addr})
		if pool.all.Count() != 0 {
			t.Fatalf("round %d: lookup holds %d txs, want 0", n, pool.all.Count())
		}
	}
	if got := pricedLen(pool); got > 16 {
		t.Fatalf("priced list retains %d entries after %d included txs; want a small bound", got, rounds)
	}
}
