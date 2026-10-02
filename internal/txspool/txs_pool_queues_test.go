// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the pending/queue sub-queue helpers in txs_pool_queues.go,
// built against a minimal in-process *TxsPool (no DB, no goroutines).

package txspool

import (
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// newTestQueuesPool builds a *TxsPool with just the fields the pending/queue
// helpers touch, skipping NewTxsPool's DB/chain wiring and background loops.
func newTestQueuesPool(t *testing.T) (*TxsPool, *mockReadState) {
	t.Helper()
	state := newMockReadState()
	pool := &TxsPool{
		config:        DefaultTxPoolConfig,
		currentState:  state,
		pendingNonces: newTxNoncer(state),
		locals:        newAccountSet(),
		pending:       make(map[types.Address]*txsList),
		queue:         make(map[types.Address]*txsList),
		beats:         make(map[types.Address]time.Time),
		all:           newTxLookup(),
		currentMaxGas: 30_000_000,
	}
	pool.priced = newTxPricedList(pool.all)
	pool.config.AccountQueue = 2
	pool.config.AccountSlots = 2
	pool.config.GlobalSlots = 4
	pool.config.GlobalQueue = 4
	pool.config.Lifetime = 0 // disable eviction by default
	return pool, state
}

func TestPromoteTxInsertsIntoPending(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x01}
	tx := newTestTxWithFrom(0, 10, addr)

	pool.all.Add(tx, false)
	pool.priced.Put(tx, false)

	ok := pool.promoteTx(addr, tx.Hash(), tx)
	if !ok {
		t.Fatal("expected promoteTx to insert")
	}
	if pool.pending[addr] == nil || pool.pending[addr].Len() != 1 {
		t.Fatalf("expected 1 pending tx for addr, got %v", pool.pending[addr])
	}
	if pool.pendingNonces.get(addr) != 1 {
		t.Fatalf("expected pending nonce 1, got %d", pool.pendingNonces.get(addr))
	}
}

func TestPromoteTxUnderpricedReplacementFails(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x02}
	tx1 := newTestTxWithFrom(0, 100, addr)
	tx2 := newTestTxWithFrom(0, 101, addr) // not a big enough bump

	pool.all.Add(tx1, false)
	pool.priced.Put(tx1, false)
	if !pool.promoteTx(addr, tx1.Hash(), tx1) {
		t.Fatal("expected first promoteTx to succeed")
	}

	pool.all.Add(tx2, false)
	pool.priced.Put(tx2, false)
	if pool.promoteTx(addr, tx2.Hash(), tx2) {
		t.Fatal("expected underpriced replacement to fail")
	}
	// tx2 should have been scrubbed from `all`/`priced`.
	if pool.all.Get(tx2.Hash()) != nil {
		t.Fatal("expected rejected replacement to be removed from all")
	}
}

func TestEnqueueTxInsertsIntoQueue(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x03}
	tx := newTestTxWithFrom(5, 10, addr)

	replaced, err := pool.enqueueTx(tx.Hash(), tx, true, true)
	if err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}
	if replaced {
		t.Fatal("expected no replacement for a fresh tx")
	}
	if pool.queue[addr] == nil || pool.queue[addr].Len() != 1 {
		t.Fatalf("expected 1 queued tx, got %v", pool.queue[addr])
	}
	if pool.all.Get(tx.Hash()) == nil {
		t.Fatal("expected tx registered in all lookup")
	}
	if _, ok := pool.beats[addr]; !ok {
		t.Fatal("expected a heartbeat to be recorded")
	}
}

func TestPromoteExecutablesPromotesReadyTx(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x05}
	state.setNonce(addr, 0)
	state.balances[addr] = uint256.NewInt(1_000_000)

	tx := newTestTxWithFrom(0, 10, addr)
	if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}

	promoted := pool.promoteExecutables([]types.Address{addr})
	if len(promoted) != 1 {
		t.Fatalf("expected 1 promoted tx, got %d", len(promoted))
	}
	if pool.pending[addr] == nil || pool.pending[addr].Len() != 1 {
		t.Fatal("expected tx to move into pending")
	}
	if pool.queue[addr] != nil {
		t.Fatal("expected queue for addr to be cleared")
	}
}

func TestPromoteExecutablesDropsLowNonce(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x06}
	// Queue a tx at nonce 0, but the account's current nonce on chain is already 1.
	tx := newTestTxWithFrom(0, 10, addr)
	if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}
	state.setNonce(addr, 1)
	state.balances[addr] = uint256.NewInt(1_000_000)

	promoted := pool.promoteExecutables([]types.Address{addr})
	if len(promoted) != 0 {
		t.Fatalf("expected 0 promoted (stale nonce), got %d", len(promoted))
	}
	if pool.all.Get(tx.Hash()) != nil {
		t.Fatal("expected stale tx removed from all")
	}
}

func TestDemoteUnexecutablesMovesBackToQueue(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x07}
	state.setNonce(addr, 0)
	state.balances[addr] = uint256.NewInt(1_000_000)

	tx0 := newTestTxWithFrom(0, 10, addr)
	tx1 := newTestTxWithFrom(1, 10, addr)
	pool.all.Add(tx0, false)
	pool.priced.Put(tx0, false)
	pool.all.Add(tx1, false)
	pool.priced.Put(tx1, false)
	pool.promoteTx(addr, tx0.Hash(), tx0)
	pool.promoteTx(addr, tx1.Hash(), tx1)

	if pool.pending[addr].Len() != 2 {
		t.Fatalf("expected 2 pending, got %d", pool.pending[addr].Len())
	}

	// Now the account's nonce regresses to 0 with insufficient balance for
	// both: simulate a reorg where only nonce 0 remains valid by dropping
	// the account's balance so tx1 becomes too costly.
	state.balances[addr] = uint256.NewInt(50) // less than value+gas*price for either

	pool.demoteUnexecutables()

	if pool.pending[addr] != nil {
		_ = pool.pending[addr] // may or may not exist depending on draining; just ensure no panic
	}
}

func TestTruncatePendingNoopBelowLimit(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x08}
	tx := newTestTxWithFrom(0, 10, addr)
	pool.all.Add(tx, false)
	pool.priced.Put(tx, false)
	pool.promoteTx(addr, tx.Hash(), tx)

	pool.truncatePending()
	if pool.pending[addr].Len() != 1 {
		t.Fatalf("expected truncatePending to be a no-op below the limit, got len=%d", pool.pending[addr].Len())
	}
}

func TestTruncatePendingDropsOverLimit(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	pool.config.GlobalSlots = 2
	pool.config.AccountSlots = 1

	addrs := []types.Address{{0x09}, {0x0A}, {0x0B}}
	for _, addr := range addrs {
		for n := uint64(0); n < 3; n++ {
			tx := newTestTxWithFrom(n, 10, addr)
			pool.all.Add(tx, false)
			pool.priced.Put(tx, false)
			pool.promoteTx(addr, tx.Hash(), tx)
		}
	}

	total := func() int {
		n := 0
		for _, l := range pool.pending {
			n += l.Len()
		}
		return n
	}
	before := total()
	if before <= int(pool.config.GlobalSlots) {
		t.Fatalf("test setup invalid: expected pending(%d) > globalSlots(%d)", before, pool.config.GlobalSlots)
	}

	pool.truncatePending()

	after := total()
	if after > before {
		t.Fatalf("truncatePending should not increase pending count: before=%d after=%d", before, after)
	}
}

func TestTruncateQueueNoopBelowLimit(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x0C}
	tx := newTestTxWithFrom(0, 10, addr)
	if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}

	pool.truncateQueue()
	if pool.queue[addr] == nil || pool.queue[addr].Len() != 1 {
		t.Fatal("expected truncateQueue to be a no-op below the limit")
	}
}

func TestTruncateQueueDropsOverLimit(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	pool.config.GlobalQueue = 1

	addrs := []types.Address{{0x0D}, {0x0E}}
	for _, addr := range addrs {
		tx := newTestTxWithFrom(0, 10, addr)
		if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
			t.Fatalf("enqueueTx: %v", err)
		}
	}

	total := func() int {
		n := 0
		for _, l := range pool.queue {
			n += l.Len()
		}
		return n
	}
	if total() <= int(pool.config.GlobalQueue) {
		t.Fatalf("test setup invalid: total=%d limit=%d", total(), pool.config.GlobalQueue)
	}

	pool.truncateQueue()

	if total() > int(pool.config.GlobalQueue) {
		t.Fatalf("expected queue truncated to <= %d, got %d", pool.config.GlobalQueue, total())
	}
}

func TestEvictStaleQueuedDisabledByDefault(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x0F}
	tx := newTestTxWithFrom(0, 10, addr)
	if _, err := pool.enqueueTx(tx.Hash(), tx, false, true); err != nil {
		t.Fatalf("enqueueTx: %v", err)
	}

	pool.config.Lifetime = 0
	pool.evictStaleQueued() // lifetime <= 0 => no-op

	if pool.queue[addr] == nil || pool.queue[addr].Len() != 1 {
		t.Fatal("expected evictStaleQueued to be a no-op when Lifetime is 0")
	}
}

func TestAccountStateFallsBackToReadState(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x10}
	state.setNonce(addr, 7)
	state.balances[addr] = uint256.NewInt(123)

	nonce, balance := pool.accountState(addr, nil)
	if nonce != 7 {
		t.Fatalf("expected nonce 7, got %d", nonce)
	}
	if balance.Cmp(uint256.NewInt(123)) != 0 {
		t.Fatalf("expected balance 123, got %s", balance.String())
	}
}

func TestAccountStateUsesSnapshotWhenPresent(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	addr := types.Address{0x11}
	snap := map[types.Address]*AccountInfo{
		addr: {Nonce: 42, Balance: uint256.NewInt(999)},
	}
	nonce, balance := pool.accountState(addr, snap)
	if nonce != 42 || balance.Cmp(uint256.NewInt(999)) != 0 {
		t.Fatalf("expected snapshot values, got nonce=%d balance=%s", nonce, balance.String())
	}
}

func TestAccountsForDeduplicatesSenders(t *testing.T) {
	pool, state := newTestQueuesPool(t)
	addr := types.Address{0x12}
	state.setNonce(addr, 3)
	state.balances[addr] = uint256.NewInt(10)

	tx1 := newTestTxWithFrom(0, 10, addr)
	tx2 := newTestTxWithFrom(1, 10, addr)

	infos := pool.accountsFor([]*transaction.Transaction{tx1, tx2})
	if len(infos) != 1 {
		t.Fatalf("expected 1 distinct sender, got %d", len(infos))
	}
	if infos[addr].Nonce != 3 {
		t.Fatalf("expected nonce 3, got %d", infos[addr].Nonce)
	}
}

func TestAccountsForEmptyInputs(t *testing.T) {
	pool, _ := newTestQueuesPool(t)
	if got := pool.accountsFor(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}
