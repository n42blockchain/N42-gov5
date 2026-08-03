// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txspool

import (
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// queuedTx builds a signed transaction sitting at a future nonce, i.e. one the
// pool can hold but never execute until the gap below it fills.
func queuedTx(t *testing.T, nonce uint64) (*transaction.Transaction, types.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := uint256.NewInt(94)
	to := types.Address{0x11}
	signer := transaction.LatestSignerForChainID(chainID.ToBig())
	tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	from, err := transaction.Sender(signer, tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.SetFrom(from)
	return tx, from
}

func newEvictionTestPool(lifetime time.Duration) *TxsPool {
	pool := &TxsPool{
		config:  TxsPoolConfig{Lifetime: lifetime, GlobalQueue: 1 << 30},
		pending: make(map[types.Address]*txsList),
		queue:   make(map[types.Address]*txsList),
		beats:   make(map[types.Address]time.Time),
		all:     newTxLookup(),
		locals:  newAccountSet(),
	}
	pool.priced = newTxPricedList(pool.all)
	return pool
}

func (pool *TxsPool) seedQueued(tx *transaction.Transaction, addr types.Address, beat time.Time) {
	list, ok := pool.queue[addr]
	if !ok {
		list = newTxsList(false)
		pool.queue[addr] = list
	}
	list.Add(tx, 0)
	pool.all.Add(tx, false)
	pool.beats[addr] = beat
}

// TestEvictStaleQueuedReclaimsStrandedTransactions is the regression for a leak
// that had no bound at all.
//
// A transaction queued above a nonce gap never becomes executable on its own.
// Nothing aged it out -- Lifetime was declared in the config and read nowhere --
// and truncateQueue, the only other exit, both exempts local accounts and only
// runs above the global queue limit. Locally submitted transactions are also
// persisted and reloaded on every start, so the set only ever grew: 118,530 of
// them on this fleet, reloaded each boot, with blocks coming out empty because
// none could be promoted.
func TestEvictStaleQueuedReclaimsStrandedTransactions(t *testing.T) {
	pool := newEvictionTestPool(time.Hour)

	stale, staleAddr := queuedTx(t, 500)
	pool.seedQueued(stale, staleAddr, time.Now().Add(-2*time.Hour))

	fresh, freshAddr := queuedTx(t, 500)
	pool.seedQueued(fresh, freshAddr, time.Now())

	pool.evictStaleQueued()

	if _, ok := pool.queue[staleAddr]; ok {
		t.Error("a queued transaction from an account silent past Lifetime was kept")
	}
	if pool.all.Get(stale.Hash()) != nil {
		t.Error("the evicted transaction is still in the lookup")
	}
	if _, ok := pool.queue[freshAddr]; !ok {
		t.Error("a recently active account lost its queued transaction")
	}
}

// TestEvictStaleQueuedCoversLocals pins the part that makes the difference: the
// stranded transactions observed in production were all local, submitted over
// RPC, and truncateQueue skips locals by design. An eviction that inherited
// that exemption would leave the leak exactly as it was.
func TestEvictStaleQueuedCoversLocals(t *testing.T) {
	pool := newEvictionTestPool(time.Hour)

	tx, addr := queuedTx(t, 900)
	pool.seedQueued(tx, addr, time.Now().Add(-3*time.Hour))
	pool.locals.add(addr)

	pool.evictStaleQueued()

	if _, ok := pool.queue[addr]; ok {
		t.Error("a stale local queued transaction was kept; the leak is unfixed")
	}
}

// TestEvictStaleQueuedDisabled keeps the escape hatch honest: Lifetime of zero
// means no ageing, which is what a caller that wants the old behaviour sets.
func TestEvictStaleQueuedDisabled(t *testing.T) {
	pool := newEvictionTestPool(0)

	tx, addr := queuedTx(t, 700)
	pool.seedQueued(tx, addr, time.Now().Add(-100*time.Hour))

	pool.evictStaleQueued()

	if _, ok := pool.queue[addr]; !ok {
		t.Error("eviction ran with Lifetime disabled")
	}
}

// TestEvictStaleQueuedAdoptsMissingHeartbeat covers an account whose heartbeat
// was never recorded -- a restore from disk, for instance. A zero timestamp is
// older than any lifetime, so treating it as an age would drop the transaction
// on sight instead of giving it its full Lifetime.
func TestEvictStaleQueuedAdoptsMissingHeartbeat(t *testing.T) {
	pool := newEvictionTestPool(time.Hour)

	tx, addr := queuedTx(t, 300)
	pool.seedQueued(tx, addr, time.Time{})
	delete(pool.beats, addr)

	pool.evictStaleQueued()

	if _, ok := pool.queue[addr]; !ok {
		t.Error("a transaction with no heartbeat was evicted immediately")
	}
	if _, ok := pool.beats[addr]; !ok {
		t.Error("eviction did not start the clock for an account with no heartbeat")
	}
}
