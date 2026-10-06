// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// mnTPrepareEnvAndState builds a fresh environment and backing
// IntraBlockState for the fixture's current head, the same way commitWork
// does before calling fillTransactions.
func mnTPrepareEnvAndState(t *testing.T, w *worker, f *mnTChainFixture) (*state.IntraBlockState, *environment) {
	t.Helper()
	env, err := w.prepareWork(&generateParams{
		timestamp: uint64(time.Now().Unix()),
		coinbase:  f.Coinbase,
	})
	if err != nil {
		t.Fatalf("prepareWork: %v", err)
	}
	tx, err := f.DB.BeginRo(w.ctx)
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	t.Cleanup(tx.Rollback)
	reader := state.NewPlainStateReader(tx)
	ibs := state.New(reader)
	return ibs, env
}

// mnTByAddr builds a Pending()-shaped map for a single sender. Per
// common.ITxsPool's contract (mirrored by builder.TxByPriceAndNonce, which
// treats slice[0] as the account's next transaction without re-sorting),
// callers must already hand transactions in ascending nonce order.
func mnTByAddr(addr types.Address, txs ...*transaction.Transaction) map[types.Address][]*transaction.Transaction {
	return map[types.Address][]*transaction.Transaction{addr: txs}
}

func TestFillTransactionsIncludesPendingInNonceOrder(t *testing.T) {
	f := mnTNewChainFixture(t)

	// Two transactions from the same sender, in nonce order as the real
	// txpool's Pending() would hand them; fillTransactions must execute both
	// and leave the block's state advanced by both nonces.
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	tx1 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(2_000))

	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)

	ibs, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0, tx1)}

	getHeader := func(hash types.Hash, number uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}

	if len(env.txs) != 2 {
		t.Fatalf("included %d txs, want 2", len(env.txs))
	}
	if env.txs[0].Hash() != tx0.Hash() || env.txs[1].Hash() != tx1.Hash() {
		t.Fatalf("transactions were not included in nonce order")
	}
	if env.tcount != 2 {
		t.Fatalf("tcount = %d, want 2", env.tcount)
	}
	if len(env.receipts) != 2 {
		t.Fatalf("receipts = %d, want 2", len(env.receipts))
	}
	wantGasUsed := env.receipts[1].CumulativeGasUsed
	if env.header.GasUsed != wantGasUsed {
		t.Fatalf("header.GasUsed = %d, want cumulative %d", env.header.GasUsed, wantGasUsed)
	}
}

func TestFillTransactionsEmptyPoolIsNoop(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	ibs, env := mnTPrepareEnvAndState(t, w, f)

	getHeader := func(hash types.Hash, number uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}
	if len(env.txs) != 0 || env.tcount != 0 {
		t.Fatalf("empty pool produced %d txs", len(env.txs))
	}
}

func TestFillTransactionsRespectsInterrupt(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))

	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	w.txsPool = &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)}
	ibs, env := mnTPrepareEnvAndState(t, w, f)

	interrupt := new(atomic.Int32)
	interrupt.Store(commitInterruptNewHead)
	getHeader := func(hash types.Hash, number uint64) *block.Header { return nil }
	err := w.fillTransactions(interrupt, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil)
	if err == nil {
		t.Fatal("expected an interrupt error")
	}
	if len(env.txs) != 0 {
		t.Fatalf("interrupted fill still included %d txs", len(env.txs))
	}
}
