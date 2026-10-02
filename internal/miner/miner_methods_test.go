// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers Miner's thin accessor/setter methods using a minimal &worker{} --
// the same lightweight-harness pattern speculative_test.go already
// established -- never going through NewMiner/newWorker (which need a real
// blockchain/txpool) and never starting any loop (isRunning() stays false).

package miner

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/miner/builder"
)

func TestMinerMiningReflectsWorkerRunning(t *testing.T) {
	// start() sends on startCh, which newWorker always creates buffered
	// (capacity 1); a bare &worker{} leaves it nil, and a send on a nil
	// channel blocks forever, so the test must supply the same buffering.
	w := &worker{startCh: make(chan struct{}, 1)}
	m := &Miner{worker: w}
	if m.Mining() {
		t.Fatalf("expected Mining() false before start")
	}
	w.start()
	t.Cleanup(w.stop)
	if !m.Mining() {
		t.Fatalf("expected Mining() true after worker.start()")
	}
}

func TestMinerSetCoinbasePropagatesToWorker(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	addr := types.HexToAddress("0x01")
	m.SetCoinbase(addr)
	if m.coinbase != addr {
		t.Fatalf("expected miner.coinbase set, got %v", m.coinbase)
	}
	w.mu.RLock()
	got := w.coinbase
	w.mu.RUnlock()
	if got != addr {
		t.Fatalf("expected worker.coinbase set, got %v", got)
	}
}

func TestMinerPendingBlockAndReceiptsEmptyByDefault(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	blk, receipts := m.PendingBlockAndReceipts()
	if blk != nil || receipts != nil {
		t.Fatalf("expected nil block/receipts with no snapshot set, got %v, %v", blk, receipts)
	}
}

func TestMinerSetZKProverService(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	called := false
	m.SetZKProverService(fakeZKProverService{fn: func() { called = true }})
	if w.zkProverService == nil {
		t.Fatalf("expected zkProverService set")
	}
	_ = w.zkProverService.SubmitBlock(types.Hash{}, 0, nil)
	if !called {
		t.Fatalf("expected the fake service to be reachable through the worker field")
	}
}

type fakeZKProverService struct{ fn func() }

func (f fakeZKProverService) SubmitBlock(blockHash types.Hash, blockNum uint64, guestInput []byte) error {
	f.fn()
	return nil
}

type fakeAIOptimizer struct{}

func (fakeAIOptimizer) OptimizeOrdering(txs []*transaction.Transaction, baseFee *uint256.Int) []*transaction.Transaction {
	return txs
}

func TestMinerSetAIOptimizer(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	m.SetAIOptimizer(fakeAIOptimizer{})
	if w.aiOptimizer == nil {
		t.Fatalf("expected aiOptimizer set on the worker")
	}
}

func TestMinerBundlePool(t *testing.T) {
	pool := &builder.BundlePool{}
	w := &worker{bundlePool: pool}
	m := &Miner{worker: w}
	if m.BundlePool() != pool {
		t.Fatalf("expected BundlePool() to return the worker's pool")
	}
}

func TestMinerSetMobilePacketSink(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	called := false
	m.SetMobilePacketSink(func(pkt *evmsdk.StreamPacket, blockNumber uint64) { called = true })
	if w.mobilePacketSink == nil {
		t.Fatalf("expected mobilePacketSink set")
	}
	w.mobilePacketSink(nil, 0)
	if !called {
		t.Fatalf("expected the sink to be reachable through the worker field")
	}
}

func TestMinerSetMobileAnchorRoot(t *testing.T) {
	w := &worker{}
	m := &Miner{worker: w}
	want := types.HexToHash("0x01")
	m.SetMobileAnchorRoot(func() *types.Hash { return &want })
	if w.mobileAnchorRoot == nil {
		t.Fatalf("expected mobileAnchorRoot set")
	}
	if got := w.mobileAnchorRoot(); got == nil || *got != want {
		t.Fatalf("expected anchor root %v, got %v", want, got)
	}
}
