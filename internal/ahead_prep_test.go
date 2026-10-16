// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/params"
)

// lagPipeAwaitPrepForTest makes the beforeSnapshot harness hold each
// parent's snapshot until its child's preparation has started.
var lagPipeAwaitPrepForTest bool

// wireTxs returns n wire transfers (no sender attached) from senders keys.
func wireTxs(t *testing.T, chainID *big.Int, frames, perFrame int) []*transaction.Transaction {
	t.Helper()
	keys := frametest.Keys(t, 41, 97)
	raws, _, _ := frametest.NewBuilder(keys[0], chainID, keys[1:]).Frames(t, frames, perFrame)
	per, dropped, err := frame.DecodeFramesEach(raws, 0)
	require.NoError(t, err)
	require.Zero(t, dropped)
	var txs []*transaction.Transaction
	for _, p := range per {
		txs = append(txs, p...)
	}
	return txs
}

// TestAheadPrepMatchesInline (P13e): the preparation of a block's parallel
// run -- every transaction's message and hash, the affinity keys and the
// first wave's partition -- is the one the run's own affinity pass
// computes, in every wave mode, and the executor adopts it.
func TestAheadPrepMatchesInline(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1337), HomesteadBlock: big.NewInt(0), ByzantiumBlock: big.NewInt(0),
		IstanbulBlock: big.NewInt(0), BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0)}
	sp := &StateProcessor{config: cfg}
	signer := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(7), 100)
	baseFee := uint256.NewInt(7)
	const workers = 16
	for _, steal := range []bool{false, true} {
		defer parallel.SetWaveModesForTest(steal, steal)()
		// Fresh wire transactions each time: the inline pass and the
		// preparation each recover the senders themselves.
		txs := wireTxs(t, cfg.ChainID, 30, 300)
		inline := make([]preparedTx, len(txs))
		ex := parallel.NewExecutor(len(txs), workers, nil)
		ex.SetAffinity(func(i int) uint64 { return sp.txAffinityKey(txs, inline, i, signer, baseFee, false) })
		ex.PrepareAffinity()
		wantKeys, wantQueues := ex.PreparedAffinityForTest()
		ex.Release()

		again := wireTxs(t, cfg.ChainID, 30, 300)
		prepared, aff := sp.prepareParallelRun(again, signer, baseFee, workers, 3)
		require.NotNil(t, aff)
		ex2 := parallel.NewExecutor(len(again), workers, nil)
		ex2.SetAffinity(func(i int) uint64 { return sp.txAffinityKey(again, prepared, i, signer, baseFee, false) })
		require.True(t, ex2.AdoptAffinity(aff))
		gotKeys, gotQueues := ex2.PreparedAffinityForTest()
		ex2.Release()

		require.Equal(t, wantKeys, gotKeys, "steal=%v: affinity keys differ", steal)
		require.Equal(t, wantQueues, gotQueues, "steal=%v: partition differs", steal)
		require.Equal(t, len(inline), len(prepared))
		for i := range inline {
			require.True(t, inline[i].ok && prepared[i].ok, "tx %d not prepared", i)
			require.Equal(t, inline[i], prepared[i], "steal=%v: tx %d message or hash differs", steal, i)
		}
	}
}

// TestAheadPrepMatchRules: a preparation is adopted only for the very
// transaction list, signer, base fee and engine presence it was computed
// for; a claim cancels one not started, and a dropped block's preparation
// is gone.
func TestAheadPrepMatchRules(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1337), LondonBlock: big.NewInt(0)}
	signer := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(7), 100)
	txs := wireTxs(t, cfg.ChainID, 1, 50)
	pr := &aheadPrep{ok: true, txs: txs, signer: signer, baseFee: uint256.NewInt(7), prepared: make([]preparedTx, len(txs))}
	require.True(t, pr.matches(txs, signer, uint256.NewInt(7), false))
	require.False(t, pr.matches(txs, signer, uint256.NewInt(8), false), "another base fee")
	require.False(t, pr.matches(txs, signer, nil, false), "no base fee")
	require.False(t, pr.matches(txs, signer, uint256.NewInt(7), true), "another engine")
	require.False(t, pr.matches(txs[:len(txs)-1], signer, uint256.NewInt(7), false), "another length")
	other := wireTxs(t, cfg.ChainID, 1, 50)
	require.False(t, pr.matches(other, signer, uint256.NewInt(7), false), "other transaction objects")
	require.False(t, pr.matches(txs, transaction.NewEIP155Signer(big.NewInt(9)), uint256.NewInt(7), false), "another signer")
	require.False(t, pr.matches(txs, nil, uint256.NewInt(7), false), "no signer")
	var none *aheadPrep
	require.False(t, none.matches(txs, signer, uint256.NewInt(7), false))
	pr.ok = false
	require.False(t, pr.matches(txs, signer, uint256.NewInt(7), false), "an unusable preparation")

	bc := &BlockChain{}
	h := types.Hash{1}
	bc.aheadPreps.m = map[types.Hash]*aheadPrep{h: {hash: h, done: make(chan struct{})}}
	got, wait := bc.claimAheadPrep(h)
	require.Nil(t, got, "a pending preparation is cancelled, not waited for")
	require.Zero(t, wait)
	require.Empty(t, bc.aheadPreps.m)

	running := &aheadPrep{hash: h, done: make(chan struct{}), ok: true}
	running.state.Store(prepRunning)
	bc.aheadPreps.m[h] = running
	go func() {
		time.Sleep(20 * time.Millisecond)
		running.state.Store(prepDone)
		close(running.done)
	}()
	got, wait = bc.claimAheadPrep(h)
	require.Same(t, running, got, "a running preparation is waited for")
	require.GreaterOrEqual(t, wait, 10*time.Millisecond)
	got, _ = bc.claimAheadPrep(h)
	require.Nil(t, got, "a preparation is claimed once")
}

// TestLagPipelineAheadPrep (P13e): with the preparation on, the queued
// children's ahead runs adopt it and still compute the serial chain's
// results; a preparation that does not match its run (another base fee)
// is ignored and the run computes its own; with it off nothing is adopted.
func TestLagPipelineAheadPrep(t *testing.T) {
	lagPipeAwaitPrepForTest = true
	defer func() { lagPipeAwaitPrepForTest = false }()
	// NewBlockChain sets the process-wide transactions-root rule from its
	// config; this file sorts before tests that rely on the default.
	prevEth, prevBlake := block.UseEthereumTxRoot, block.TxRootBlake3Time
	defer func() { block.UseEthereumTxRoot, block.TxRootBlake3Time = prevEth, prevBlake }()
	run := func(t *testing.T) (hits, misses uint64) {
		h0, m0 := AheadPrepStats()
		testLagPipelineAhead(t, false, arrivalBeforeSnapshot)
		h1, m1 := AheadPrepStats()
		return h1 - h0, m1 - m0
	}
	t.Run("on", func(t *testing.T) {
		defer SetAheadPrepForTest(true)()
		hits, misses := run(t)
		t.Logf("preparations adopted %d, not %d", hits, misses)
		require.GreaterOrEqual(t, hits, uint64(8), "the ahead runs did not adopt their preparations")
	})
	t.Run("mismatch", func(t *testing.T) {
		defer SetAheadPrepForTest(true)()
		aheadPrepDoneForTest = func(pr *aheadPrep) {
			if pr.baseFee == nil {
				pr.baseFee = uint256.NewInt(1)
			} else {
				pr.baseFee = new(uint256.Int).AddUint64(pr.baseFee, 1)
			}
		}
		defer func() { aheadPrepDoneForTest = nil }()
		hits, misses := run(t)
		require.Zero(t, hits, "a mismatched preparation was adopted")
		require.NotZero(t, misses)
	})
	t.Run("off", func(t *testing.T) {
		defer SetAheadPrepForTest(false)()
		hits, misses := run(t)
		require.Zero(t, hits)
		require.NotZero(t, misses)
	})
}
