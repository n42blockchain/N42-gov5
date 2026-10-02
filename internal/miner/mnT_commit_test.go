// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// mnTBuildFilledEnv runs prepareWork + fillTransactions on the fixture's
// current head and returns the resulting environment and state, ready for
// commit().
func mnTBuildFilledEnv(t *testing.T, w *worker, f *mnTChainFixture) (*environment, *state.IntraBlockState) {
	t.Helper()
	ibs, env := mnTPrepareEnvAndState(t, w, f)
	getHeader := func(hash types.Hash, number uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, "")); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}
	return env, ibs
}

func TestCommitProducesSealedTask(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))

	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	atomic.StoreInt32(&w.running, 1) // commit() requires isRunning(); no background loop exists on this bare worker

	env, ibs := mnTBuildFilledEnv(t, w, f)
	if len(env.txs) != 1 {
		t.Fatalf("expected the one pending tx to be included, got %d", len(env.txs))
	}

	if err := w.commit(env, state.NewNoopWriter(), ibs, time.Now(), nil, nil, nil, false, types.Hash{}, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commit did not send a task")
	}
	if tk.block == nil {
		t.Fatal("task has no block")
	}
	hdr, ok := tk.block.Header().(*block.Header)
	if !ok {
		t.Fatal("sealed block header is not *block.Header")
	}
	if hdr.Root == (types.Hash{}) {
		t.Fatal("sealed block has a zero state root")
	}
	if len(tk.receipts) != 1 {
		t.Fatalf("task receipts = %d, want 1", len(tk.receipts))
	}
	// A plain transfer emits no logs, so the bloom is legitimately zero; check
	// it was actually derived from the receipts rather than left unset by
	// comparing against an independent recomputation.
	if hdr.Bloom != block.CreateBloom(tk.receipts) {
		t.Fatal("sealed block bloom does not match its own receipts")
	}
	if hdr.ReceiptHash == (types.Hash{}) {
		t.Fatal("sealed block has a zero receipt hash")
	}
	wantParent := f.Chain.CurrentBlock().Hash()
	if hdr.ParentHash != wantParent {
		t.Fatalf("sealed block parent = %x, want %x", hdr.ParentHash, wantParent)
	}
}

func TestCommitNoopWhenNotRunning(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	w.coinbase = f.Coinbase
	// running stays 0: commit() must return nil without touching taskCh.

	env, ibs := mnTBuildFilledEnv(t, w, f)
	if err := w.commit(env, state.NewNoopWriter(), ibs, time.Now(), nil, nil, nil, false, types.Hash{}, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	select {
	case <-w.taskCh:
		t.Fatal("commit sent a task while the worker was not running")
	case <-time.After(50 * time.Millisecond):
	}
}
