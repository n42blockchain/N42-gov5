// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// mnTNewIdleWorker builds a real worker via newWorker(init=false) on top of
// the given fixture's chain, and registers a cleanup that cancels it. init=
// false means the start signal is never sent on startCh, so workLoop/runLoop
// never see "running"=1 and commitWork/fillTransactions are only driven
// directly by the test, never by the background loops.
// mnTBareWorker builds a worker struct literal directly, like
// speculative_test.go's `&worker{}` tests do, with no newWorker call and so
// no background goroutines (workLoop/runLoop/taskLoop/resultLoop). Needed
// whenever a test sets running=1 and reads w.taskCh itself: newWorker's
// taskLoop would otherwise race the test for the same send and run a real
// Seal+write, and its workLoop ticker can independently trigger an unwanted
// second (empty) build once the worker is "running". Has just enough state
// for prepareWork/makeEnv/fillTransactions/commit to run.
func mnTBareWorker(tb testing.TB, f *mnTChainFixture, pool common.ITxsPool) *worker {
	tb.Helper()
	w := &worker{
		engine:         f.Engine,
		chain:          f.Chain,
		txsPool:        pool,
		chainConfig:    f.Config,
		minerConf:      conf.MinerConfig{GasCeil: 30_000_000},
		taskCh:         make(chan *task, 1),
		resultCh:       make(chan block.IBlock, 1),
		ctx:            context.Background(),
		pendingTasks:   make(map[types.Hash]*task),
		sealedOnParent: make(map[types.Hash]block.IBlock),
		sealedByHash:   make(map[types.Hash]block.IBlock),
		sealedPost:     make(map[types.Hash]*state.PostState),
		sealedExec:     make(map[types.Hash]rawdb.ExecutedResult),
	}
	return w
}

func mnTNewIdleWorker(tb testing.TB, f *mnTChainFixture, pool *mnTStubTxsPool) *worker {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	group, gctx := errgroup.WithContext(ctx)
	w := newWorker(gctx, group, f.Config, f.Engine, f.Chain, pool, nil, false, conf.MinerConfig{GasCeil: 30_000_000})
	tb.Cleanup(func() {
		w.close()
		cancel()
		_ = group.Wait()
	})
	return w
}

func TestNewWorkerIdleDoesNotProduce(t *testing.T) {
	f := mnTNewChainFixture(t)
	pool := &mnTStubTxsPool{}
	w := mnTNewIdleWorker(t, f, pool)

	if w.isRunning() {
		t.Fatalf("worker started running with init=false")
	}
	w.setCoinbase(f.Coinbase)
	if w.coinbase != f.Coinbase {
		t.Fatalf("setCoinbase did not take effect")
	}

	// The background loops are alive (newWorker always starts them) but idle:
	// give them a moment and confirm no block was produced on the chain.
	select {
	case <-time.After(50 * time.Millisecond):
	}
	if w.chain.CurrentBlock().Number64().Uint64() != 0 {
		t.Fatalf("idle worker advanced the chain head")
	}
}

func TestPrepareWorkAndMakeEnv(t *testing.T) {
	f := mnTNewChainFixture(t)
	pool := &mnTStubTxsPool{}
	w := mnTNewIdleWorker(t, f, pool)
	w.setCoinbase(f.Coinbase)

	parentBlock := f.Chain.CurrentBlock()
	parentHeader, ok := parentBlock.Header().(*block.Header)
	if !ok {
		t.Fatalf("chain head header is not *block.Header")
	}

	env, err := w.prepareWork(&generateParams{
		timestamp: uint64(time.Now().Unix()),
		coinbase:  f.Coinbase,
	})
	if err != nil {
		t.Fatalf("prepareWork: %v", err)
	}
	if env == nil {
		t.Fatal("prepareWork returned nil environment")
	}
	if env.header.ParentHash != parentBlock.Hash() {
		t.Fatalf("env header parent = %x, want %x", env.header.ParentHash, parentBlock.Hash())
	}
	if env.header.Number.Uint64() != parentHeader.Number.Uint64()+1 {
		t.Fatalf("env header number = %d, want %d", env.header.Number.Uint64(), parentHeader.Number.Uint64()+1)
	}
	if env.header.Coinbase != f.Coinbase {
		t.Fatalf("env header coinbase = %x, want %x", env.header.Coinbase, f.Coinbase)
	}
	wantGasLimit := CalcGasLimit(parentHeader.GasLimit, w.minerConf.GasCeil)
	if env.header.GasLimit != wantGasLimit {
		t.Fatalf("env header gas limit = %d, want %d", env.header.GasLimit, wantGasLimit)
	}
	if env.gasPool == nil || env.gasPool.Gas() != fillGasBudget(env.header.GasLimit) {
		t.Fatalf("env gas pool not seeded from fillGasBudget")
	}
	if env.ancestors == nil || env.family == nil {
		t.Fatalf("makeEnv did not initialise ancestor/family sets")
	}

	// makeEnv directly, to also exercise the explicit parent/header path.
	env2 := w.makeEnv(parentHeader, env.header, f.Coinbase)
	if env2.coinbase != f.Coinbase {
		t.Fatalf("makeEnv coinbase mismatch")
	}
}

func TestPrepareWorkMissingParentErrors(t *testing.T) {
	f := mnTNewChainFixture(t)
	pool := &mnTStubTxsPool{}
	w := mnTNewIdleWorker(t, f, pool)
	w.setCoinbase(f.Coinbase)

	_, err := w.prepareWork(&generateParams{
		timestamp:  uint64(time.Now().Unix()),
		coinbase:   f.Coinbase,
		parentHash: types.Hash{0xde, 0xad},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown pinned parent")
	}
}
