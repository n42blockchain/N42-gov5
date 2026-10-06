// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers shouldProduceNow (timer-driven vs leader-aware engines) and
// environment.copy/copyReceipts, all pure logic reachable without starting
// any worker loop.

package miner

import (
	"testing"

	mapset "github.com/deckarep/golang-set"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/params"
)

func TestShouldProduceNowNilEngineAlwaysTrue(t *testing.T) {
	w := &worker{}
	if !w.shouldProduceNow() {
		t.Fatalf("expected a nil engine to always produce")
	}
}

func TestShouldProduceNowTimerDrivenEngineAlwaysTrue(t *testing.T) {
	w := &worker{engine: apos.NewFaker()} // apos.Faker is not HotStuff -> timer-driven
	if !w.shouldProduceNow() {
		t.Fatalf("expected a timer-driven engine to always produce")
	}
}

// fakeLeaderEngine is a minimal consensus.Engine (embeds nil, only Type and
// IsCurrentLeader are ever called by shouldProduceNow) that reports
// HotStuffConsensus (not timer-driven) and a settable leader flag.
type fakeLeaderEngine struct {
	consensus.Engine
	isLeader bool
}

func (f fakeLeaderEngine) Type() params.ConsensusType { return params.HotStuffConsensus }
func (f fakeLeaderEngine) IsCurrentLeader() bool       { return f.isLeader }

var _ leaderAware = fakeLeaderEngine{}

func TestShouldProduceNowLeaderAwareEngineChecksLeadership(t *testing.T) {
	wLeader := &worker{engine: fakeLeaderEngine{isLeader: true}}
	if !wLeader.shouldProduceNow() {
		t.Fatalf("expected the current leader to produce")
	}
	wFollower := &worker{engine: fakeLeaderEngine{isLeader: false}}
	if wFollower.shouldProduceNow() {
		t.Fatalf("expected a non-leader to not produce")
	}
}

// fakeNonLeaderAwareEngine is HotStuff-typed but does NOT implement
// leaderAware, exercising shouldProduceNow's final fallback (return true).
type fakeNonLeaderAwareEngine struct{ consensus.Engine }

func (f fakeNonLeaderAwareEngine) Type() params.ConsensusType { return params.HotStuffConsensus }

func TestShouldProduceNowNonLeaderAwareEngineFallsBackTrue(t *testing.T) {
	w := &worker{engine: fakeNonLeaderAwareEngine{}}
	if !w.shouldProduceNow() {
		t.Fatalf("expected the fallback case to produce")
	}
}

func TestEnvironmentCopyIsIndependent(t *testing.T) {
	addr := types.HexToAddress("0x01")
	env := &environment{
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		tcount:    3,
		gasPool:   new(common.GasPool).AddGas(1000),
		coinbase:  addr,
		header:    &block.Header{Number: uint256.NewInt(1), GasUsed: 10},
		txs:       []*transaction.Transaction{nil, nil},
		receipts:  []*block.Receipt{{GasUsed: 21000}},
	}
	env.ancestors.Add("a")
	env.family.Add("f")

	cpy := env.copy()

	// Mutate the original after copying; the copy must not see the change.
	env.ancestors.Add("b")
	env.header.GasUsed = 999
	env.receipts[0].GasUsed = 1

	if cpy.ancestors.Contains("b") {
		t.Fatalf("copy shares the ancestors set with the original")
	}
	if cpy.header.GasUsed == 999 {
		t.Fatalf("copy shares the header with the original")
	}
	if cpy.receipts[0].GasUsed == 1 {
		t.Fatalf("copy shares receipt pointers with the original")
	}
	if cpy.tcount != 3 || cpy.coinbase != addr {
		t.Fatalf("copy dropped scalar fields: tcount=%d coinbase=%v", cpy.tcount, cpy.coinbase)
	}
	if cpy.gasPool == nil || *cpy.gasPool != *env.gasPool {
		t.Fatalf("expected gasPool value-copied")
	}
	if len(cpy.txs) != 2 {
		t.Fatalf("expected txs slice copied, got len %d", len(cpy.txs))
	}
}

func TestEnvironmentCopyNilGasPool(t *testing.T) {
	env := &environment{
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		header:    &block.Header{Number: uint256.NewInt(1)},
	}
	cpy := env.copy()
	if cpy.gasPool != nil {
		t.Fatalf("expected nil gasPool to stay nil")
	}
}

func TestCopyReceiptsIndependent(t *testing.T) {
	original := []*block.Receipt{{GasUsed: 100}, {GasUsed: 200}}
	cpy := copyReceipts(original)
	if len(cpy) != 2 {
		t.Fatalf("expected 2 receipts, got %d", len(cpy))
	}
	cpy[0].GasUsed = 999
	if original[0].GasUsed == 999 {
		t.Fatalf("copyReceipts shares receipt pointers with the input")
	}
}
