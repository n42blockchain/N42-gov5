// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

// mnTNewMiner builds a real *Miner over the fixture's chain via NewMiner,
// registering a cleanup that closes it.
func mnTNewMiner(tb testing.TB, f *mnTChainFixture, pool *mnTStubTxsPool) *Miner {
	tb.Helper()
	cfg := &conf.Config{
		ChainCfg: f.Config,
		Miner:    conf.MinerConfig{GasCeil: 30_000_000},
	}
	m := NewMiner(context.Background(), cfg, f.Chain, f.Engine, pool, nil)
	tb.Cleanup(m.Close)
	return m
}

func TestNewMinerStartCloseNotMining(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	if m.Mining() {
		t.Fatal("a freshly built miner reports Mining() before Start")
	}

	m.SetCoinbase(f.Coinbase)

	m.Start()
	// Start() does not itself flip "running" -- that only happens once a
	// downloader-finished event arrives (see runLoop); a miner that never
	// sees one never mines, which is exactly the "not mining" case this
	// test is for.
	select {
	case <-time.After(50 * time.Millisecond):
	}
	if m.Mining() {
		t.Fatal("miner started mining without a downloader-finished event")
	}
	if f.Chain.CurrentBlock().Number64().Uint64() != 0 {
		t.Fatal("a non-mining miner advanced the chain head")
	}
}

func TestMinerPendingBlockAndReceiptsEmpty(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	blk, receipts := m.PendingBlockAndReceipts()
	if blk != nil || receipts != nil {
		t.Fatalf("expected no pending block/receipts before any build, got block=%v receipts=%v", blk, receipts)
	}
}

func TestMinerTriggerBlockProductionWhileNotRunning(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	// isRunning() is false (worker never started): TriggerBlockProduction
	// must warn and return without touching newWorkCh.
	m.TriggerBlockProduction(types.Hash{0x01})
	select {
	case <-m.worker.newWorkCh:
		t.Fatal("a trigger while not running queued a build request")
	default:
	}
}

func TestMinerPrepareSpeculativeBlockWhileNotRunning(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	m.PrepareSpeculativeBlock(types.Hash{0x02})
	select {
	case <-m.worker.newWorkCh:
		t.Fatal("a speculative request while not running queued a build")
	default:
	}

	// A zero parent hash is also rejected, running or not.
	m.PrepareSpeculativeBlock(types.Hash{})
	select {
	case <-m.worker.newWorkCh:
		t.Fatal("a zero-parent speculative request queued a build")
	default:
	}
}

func TestMinerCommitToCanonicalKnownBlock(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	// *internal.BlockChain implements the canonicalCommitter interfaces, so
	// both forms forward to it instead of falling back to a nil no-op; the
	// genesis hash is already in the db and already canonical.
	genesisHash := f.Chain.CurrentBlock().Hash()
	if err := m.CommitToCanonical(genesisHash); err != nil {
		t.Fatalf("CommitToCanonical: %v", err)
	}
	if err := m.CommitToCanonicalWith(genesisHash, nil); err != nil {
		t.Fatalf("CommitToCanonicalWith: %v", err)
	}

	// An unknown hash surfaces the chain's own error rather than being
	// silently swallowed.
	if err := m.CommitToCanonical(types.Hash{0x03}); err == nil {
		t.Fatal("expected an error committing an unknown block to canonical")
	}
}

func TestMinerSetAIOptimizerAndBundlePool(t *testing.T) {
	f := mnTNewChainFixture(t)
	m := mnTNewMiner(t, f, &mnTStubTxsPool{})

	if m.BundlePool() == nil {
		t.Fatal("NewMiner did not wire a bundle pool")
	}

	opt := &mnTFakeOptimizer{}
	m.SetAIOptimizer(opt)
	if m.worker.aiOptimizer != opt {
		t.Fatal("SetAIOptimizer did not set worker.aiOptimizer")
	}
}

// mnTFakeOptimizer is a trivial AIOptimizer: it reverses the given slice.
type mnTFakeOptimizer struct{}

func (mnTFakeOptimizer) OptimizeOrdering(txs []*transaction.Transaction, baseFee *uint256.Int) []*transaction.Transaction {
	out := make([]*transaction.Transaction, len(txs))
	for i, tx := range txs {
		out[len(txs)-1-i] = tx
	}
	return out
}
