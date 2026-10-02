// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// mnTSealOneBlock builds, fills and commits a single block from the
// fixture's current head with the given pending transactions, returning the
// resulting task as commit() sent it (synchronously drained from w.taskCh).
func mnTSealOneBlock(t *testing.T, w *worker, f *mnTChainFixture) *task {
	t.Helper()
	atomic.StoreInt32(&w.running, 1)
	env, ibs := mnTBuildFilledEnv(t, w, f)
	if err := w.commit(env, state.NewNoopWriter(), ibs, time.Now(), nil, nil, nil, false, types.Hash{}, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	select {
	case tk := <-w.taskCh:
		return tk
	default:
		t.Fatal("commit did not send a task")
		return nil
	}
}

func TestHandleSealedAdvancesChainHead(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))

	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase

	tk := mnTSealOneBlock(t, w, f)
	sealhash := w.engine.SealHash(tk.block.Header())
	w.mu.Lock()
	w.pendingTasks[sealhash] = tk
	w.mu.Unlock()

	before := f.Chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(tk.block)

	after := f.Chain.CurrentBlock()
	if after.Number64().Uint64() != before+1 {
		t.Fatalf("chain head = %d, want %d", after.Number64().Uint64(), before+1)
	}
	if after.Hash() != tk.block.Hash() {
		t.Fatalf("chain head hash = %x, want %x", after.Hash(), tk.block.Hash())
	}

	// The pending task entry is consumed by a successful write.
	w.mu.RLock()
	_, stillPending := w.pendingTasks[sealhash]
	w.mu.RUnlock()
	if stillPending {
		t.Fatal("pendingTasks entry survived a successful write")
	}
}

func TestHandleSealedNilBlockIsNoop(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	before := f.Chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(nil)
	if f.Chain.CurrentBlock().Number64().Uint64() != before {
		t.Fatal("handleSealed(nil) advanced the chain head")
	}
}

func TestHandleSealedMissingPendingTaskIsDropped(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase

	tk := mnTSealOneBlock(t, w, f)
	// Deliberately do not register tk in w.pendingTasks.

	before := f.Chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(tk.block)
	if f.Chain.CurrentBlock().Number64().Uint64() != before {
		t.Fatal("handleSealed wrote a block with no matching pending task")
	}
}

func TestHandleSealedDuplicateIsIgnored(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase

	tk := mnTSealOneBlock(t, w, f)
	sealhash := w.engine.SealHash(tk.block.Header())
	w.mu.Lock()
	w.pendingTasks[sealhash] = tk
	w.mu.Unlock()
	w.handleSealed(tk.block)

	afterFirst := f.Chain.CurrentBlock().Number64().Uint64()

	// A second delivery of the SAME already-imported block must be a no-op
	// (handleSealed's HasBlock short-circuit), not an error or a reorg.
	w.handleSealed(tk.block)
	if f.Chain.CurrentBlock().Number64().Uint64() != afterFirst {
		t.Fatal("re-delivering an already-imported sealed block changed the head")
	}
}
