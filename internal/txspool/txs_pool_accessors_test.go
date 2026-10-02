// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for TxsPool's read-only accessors (Has, GetTx, Pending, Content,
// Nonce, Stats, ResetState), built on the same minimal pool used by
// txs_pool_queues_test.go.

package txspool

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestHasAndGetTx(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x20}
	tx := newTestTxWithFrom(0, 10, addr)

	if pool.Has(tx.Hash()) {
		t.Fatal("expected Has to be false before insertion")
	}
	pool.all.Add(tx, false)
	if !pool.Has(tx.Hash()) {
		t.Fatal("expected Has to be true after insertion")
	}
	if got := pool.GetTx(tx.Hash()); got != tx {
		t.Fatalf("GetTx returned wrong transaction: %v", got)
	}
	if got := pool.GetTx(types.Hash{0xFF}); got != nil {
		t.Fatalf("expected nil for unknown hash, got %v", got)
	}
}

func TestPendingAndGetTransaction(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x21}
	tx := newTestTxWithFrom(0, 10, addr)
	pool.all.Add(tx, false)
	pool.priced.Put(tx, false)
	pool.promoteTx(addr, tx.Hash(), tx)

	pending := pool.Pending(false)
	if len(pending[addr]) != 1 {
		t.Fatalf("expected 1 pending tx for addr, got %d", len(pending[addr]))
	}

	txs, err := pool.GetTransaction()
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("expected 1 tx overall, got %d", len(txs))
	}
}

func TestContentSeparatesPendingAndQueued(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	pendingAddr := types.Address{0x22}
	queuedAddr := types.Address{0x23}

	ptx := newTestTxWithFrom(0, 10, pendingAddr)
	pool.all.Add(ptx, false)
	pool.priced.Put(ptx, false)
	pool.promoteTx(pendingAddr, ptx.Hash(), ptx)

	qtx := newTestTxWithFrom(5, 10, queuedAddr)
	if _, err := pool.enqueueTx(qtx.Hash(), qtx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}

	pending, queued := pool.Content()
	if len(pending[pendingAddr]) != 1 {
		t.Fatalf("expected 1 pending tx, got %d", len(pending[pendingAddr]))
	}
	if len(queued[queuedAddr]) != 1 {
		t.Fatalf("expected 1 queued tx, got %d", len(queued[queuedAddr]))
	}
}

func TestNonceReadOnly(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x24}
	state.setNonce(addr, 9)

	if got := pool.Nonce(addr); got != 9 {
		t.Fatalf("expected nonce 9, got %d", got)
	}
	// getReadOnly must not cache: a second read-only query for an address
	// never promoted into pendingNonces should still reflect state changes.
	state.setNonce(addr, 10)
	if got := pool.Nonce(addr); got != 10 {
		t.Fatalf("expected nonce 10 after state change, got %d", got)
	}
}

func TestStatsAndStatsPrint(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x25}
	tx := newTestTxWithFrom(0, 10, addr)
	pool.all.Add(tx, false)
	pool.priced.Put(tx, false)
	pool.promoteTx(addr, tx.Hash(), tx)

	qaddr := types.Address{0x26}
	qtx := newTestTxWithFrom(5, 10, qaddr)
	if _, err := pool.enqueueTx(qtx.Hash(), qtx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}

	pendingAddrs, pendingTxs, queuedAddrs, queuedTxs := pool.Stats()
	if pendingAddrs != 1 || pendingTxs != 1 {
		t.Fatalf("pending stats mismatch: addrs=%d txs=%d", pendingAddrs, pendingTxs)
	}
	if queuedAddrs != 1 || queuedTxs != 1 {
		t.Fatalf("queued stats mismatch: addrs=%d txs=%d", queuedAddrs, queuedTxs)
	}

	// StatsPrint just logs; make sure it doesn't panic.
	pool.StatsPrint()
}

func TestResetStateIsNoop(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	if err := pool.ResetState(types.Hash{0x01}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
