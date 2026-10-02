// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func TestCommitWorkEndToEnd(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))

	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
	atomic.StoreInt32(&w.running, 1)

	if err := w.commitWork(nil, false, time.Now().Unix(), types.Hash{}, false, time.Time{}); err != nil {
		t.Fatalf("commitWork: %v", err)
	}

	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commitWork did not send a task")
	}
	if tk.block == nil {
		t.Fatal("task has no block")
	}
	if len(tk.receipts) != 1 {
		t.Fatalf("task receipts = %d, want 1", len(tk.receipts))
	}
	wantParent := f.Chain.CurrentBlock().Hash()
	if tk.block.ParentHash() != wantParent {
		t.Fatalf("built block's parent = %x, want %x", tk.block.ParentHash(), wantParent)
	}
}

func TestCommitWorkRequiresCoinbaseWhileRunning(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	atomic.StoreInt32(&w.running, 1)
	// coinbase left at the zero address.

	err := w.commitWork(nil, false, time.Now().Unix(), types.Hash{}, false, time.Time{})
	if err == nil {
		t.Fatal("expected an error for an empty coinbase while running")
	}
}
