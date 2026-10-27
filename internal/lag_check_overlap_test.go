// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// lagPipeCheckForTest, when set, replaces the pipeline harness's
// availability check of each piped block (tests: the push path's order).
var lagPipeCheckForTest func(bc *BlockChain, b *block.Block) (checked, retry bool, err error)

// awaitAheadOf waits until b's ahead run, if one is launched, finished, and
// reports whether one did.
func awaitAheadOf(bc *BlockChain, b *block.Block, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		bc.lagPipe.mu.Lock()
		a := bc.lagPipe.spec
		bc.lagPipe.mu.Unlock()
		if a != nil && a.blk == b {
			select {
			case <-a.done:
				return true
			case <-time.After(time.Until(deadline)):
				return false
			}
		}
		time.Sleep(200 * time.Microsecond)
	}
	return false
}

// TestLagPushCheckOverlap (N42_PUSH_CHECK_OVERLAP): the pipeline harness
// with each block arriving the push path's way -- queued unchecked
// (LagEnqueueUnchecked), then checked, then dropped if still unchecked
// (LagDropUnchecked) -- with the overlap off and on. Every block's executed
// result must equal the serial chain's (the harness checks) and the
// flag-off run's, and so must the committed rows. Cases:
//
//   - on: the check right after the enqueue;
//   - slowCheck: each check starts only after the block's ahead run
//     finished (or 40 ms): the executor must hold the block -- not stored,
//     not imported -- until the check passes;
//   - checkFail: block 7's check fails once after its ahead run finished:
//     it is dropped with its ahead run (aheadDiscForget), nothing is stored
//     or imported, and the chain recovers when the block is checked again
//     (the gossip copy's check) and imported in order.
func TestLagPushCheckOverlap(t *testing.T) {
	lagPipeAwaitPrepForTest = true
	defer func() { lagPipeAwaitPrepForTest = false }()
	prevEth, prevBlake := block.UseEthereumTxRoot, block.TxRootBlake3Time
	defer func() { block.UseEthereumTxRoot, block.TxRootBlake3Time = prevEth, prevBlake }()
	defer SetAheadPrepForTest(true)()
	lagPipeRotateToForTest = true
	defer func() { lagPipeRotateToForTest = false }()

	type out struct {
		results                         map[uint64]rawdb.ExecutedResult
		rows                            map[string][32]byte
		queued, passed, dropped, heldMs uint64
	}
	run := func(t *testing.T, on bool, before func(t *testing.T, bc *BlockChain, b *block.Block, queued bool) (fail bool)) out {
		defer SetPushCheckOverlapForTest(on)()
		res := out{results: make(map[uint64]rawdb.ExecutedResult)}
		lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { res.results[n] = r }
		defer func() { lagPipeResultSinkForTest = nil }()
		lagPipeChainSinkForTest = func(lc *lagChain) { res.rows = lagChainDigest(t, lc) }
		defer func() { lagPipeChainSinkForTest = nil }()
		lagPipeCheckForTest = func(bc *BlockChain, b *block.Block) (bool, bool, error) {
			queued := bc.LagEnqueueUnchecked(b)
			require.Equal(t, on, queued, "block %d: unchecked enqueue", b.Number64().Uint64())
			require.False(t, bc.LagImportQueued(b.Hash()), "an unchecked block reads as queued")
			fail := false
			if before != nil {
				fail = before(t, bc, b, queued)
			}
			var discBefore uint64
			if fail {
				h := b.Hash()
				lagCheckFailForTest.Store(&h)
				discBefore = bc.lagPipe.discForget.Load()
			}
			checked, retry, err := bc.CheckDeferredBlock(b)
			if queued {
				require.Equal(t, err != nil, bc.LagDropUnchecked(b.Hash()), "block %d: drop after the check (err %v)", b.Number64().Uint64(), err)
			}
			if fail {
				require.Error(t, err)
				n := b.Number64().Uint64()
				require.False(t, bc.blockStored(b.Hash(), n), "a block whose check failed was written")
				_, last, _ := bc.LagStats()
				require.Less(t, last, n, "a block whose check failed was imported")
				_, pending := bc.pendingExecutedResult(b.Hash())
				require.False(t, pending, "a block whose check failed has a result")
				require.Nil(t, bc.AvailableBlock(b.Hash()), "a block whose check failed is available")
				// Its ahead run is discarded, and nothing runs ahead on it.
				require.Greater(t, bc.lagPipe.discForget.Load(), discBefore, "the failed block's ahead run was not discarded")
				bc.lagPipe.mu.Lock()
				if a := bc.lagPipe.spec; a != nil {
					require.NotEqual(t, b.Hash(), a.blk.Hash(), "the failed block's ahead run survived")
					require.NotEqual(t, b.Hash(), a.parent, "a child runs ahead on the failed block")
				}
				bc.lagPipe.mu.Unlock()
				// The gossip copy's check: the normal path.
				return bc.CheckDeferredBlock(b)
			}
			return checked, retry, err
		}
		defer func() { lagPipeCheckForTest = nil }()
		q0, p0, d0, h0 := CheckOverlapStats()
		testLagPipelineAhead(t, false, arrivalBeforeSnapshot)
		q1, p1, d1, h1 := CheckOverlapStats()
		res.queued, res.passed, res.dropped, res.heldMs = q1-q0, p1-p0, d1-d0, h1-h0
		t.Logf("overlap: queued %d, passed %d, dropped %d, held %d ms", res.queued, res.passed, res.dropped, res.heldMs)
		require.NotEmpty(t, res.results)
		return res
	}
	same := func(t *testing.T, ref, got out) {
		t.Helper()
		require.Equal(t, len(ref.results), len(got.results))
		for n, r := range ref.results {
			require.Equal(t, r, got.results[n], "block %d: result differs with the overlap", n)
		}
		require.Equal(t, ref.rows, got.rows, "committed rows differ with the overlap")
	}

	var ref out
	t.Run("off", func(t *testing.T) {
		ref = run(t, false, nil)
		require.Zero(t, ref.queued)
	})
	t.Run("on", func(t *testing.T) {
		got := run(t, true, nil)
		same(t, ref, got)
		require.GreaterOrEqual(t, got.queued, uint64(12))
		require.Equal(t, got.queued, got.passed)
		require.Zero(t, got.dropped)
	})
	t.Run("slowCheck", func(t *testing.T) {
		held := 0
		got := run(t, true, func(t *testing.T, bc *BlockChain, b *block.Block, queued bool) bool {
			if awaitAheadOf(bc, b, 40*time.Millisecond) {
				// The executor finished the block's execution: it holds it
				// until the check passes.
				time.Sleep(5 * time.Millisecond)
				n := b.Number64().Uint64()
				require.False(t, bc.blockStored(b.Hash(), n), "block %d written before its check", n)
				_, last, _ := bc.LagStats()
				require.Less(t, last, n, "block %d imported before its check", n)
				held++
			}
			return false
		})
		same(t, ref, got)
		t.Logf("blocks executed ahead before their check: %d", held)
		require.GreaterOrEqual(t, held, 6, "the ahead runs did not finish before the slow checks")
		require.Equal(t, got.queued, got.passed)
		require.NotZero(t, got.heldMs, "the executor never held a block for its check")
	})
	t.Run("checkFail", func(t *testing.T) {
		var ahead bool
		got := run(t, true, func(t *testing.T, bc *BlockChain, b *block.Block, queued bool) bool {
			if b.Number64().Uint64() != 7 {
				return false
			}
			ahead = awaitAheadOf(bc, b, 2*time.Second)
			return true
		})
		same(t, ref, got)
		require.True(t, ahead, "block 7 did not run ahead before its failing check")
		require.Equal(t, uint64(1), got.dropped)
		require.Equal(t, got.queued, got.passed+got.dropped)
	})
}
