// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// lagPipeAwaitBaseForTest makes the beforeSnapshot harness (with
// lagPipeAwaitPrepForTest) hold each parent's snapshot until its child's
// base prefill finished: it read the tree before the parent's root.
var lagPipeAwaitBaseForTest bool

// lagPipeRotateToForTest makes the pipeline harness send block n's
// transfers to one of three recipients, n mod 3: each child credits an
// account its parent did not write.
var lagPipeRotateToForTest bool

// awaitAheadBase waits until the preparation of h, if registered, and its
// base prefill, if started, are done.
func awaitAheadBase(t *testing.T, bc *BlockChain, h types.Hash) {
	t.Helper()
	bc.aheadPreps.mu.Lock()
	pr := bc.aheadPreps.m[h]
	bc.aheadPreps.mu.Unlock()
	if pr == nil || pr.state.Load() == prepCancelled {
		return
	}
	select {
	case <-pr.done:
	case <-time.After(10 * time.Second):
		t.Errorf("preparation of %x never finished", h[:4])
		return
	}
	if pr.base == nil {
		return
	}
	select {
	case <-pr.base.done:
	case <-time.After(15 * time.Second):
		t.Errorf("base prefill of %x never finished", h[:4])
	}
}

// TestLagPipelineAheadBasePrefill (P15): the queued children's ahead runs
// read their base through a prefill and still compute the serial chain's
// results block for block (testLagPipelineAhead compares them). Every
// sender sends in every block, so every prefilled sender is a key the
// parent wrote: its value must come from the parent's post-state, never
// from the prefill; each block's recipient was last written two blocks
// earlier, so its value must come from the prefill (or the tree).
//
//   - on: the default arrival before the snapshot;
//   - grandparent: each prefill reads the tree before the parent's root
//     (the snapshot waits for it) and its run reads only after that root
//     applied, so the prefill holds the grandparent's values throughout;
//   - off: N42_AHEAD_BASE_PREFETCH=0 reads nothing through a prefill.
func TestLagPipelineAheadBasePrefill(t *testing.T) {
	lagPipeAwaitPrepForTest = true
	defer func() { lagPipeAwaitPrepForTest = false }()
	prevEth, prevBlake := block.UseEthereumTxRoot, block.TxRootBlake3Time
	defer func() { block.UseEthereumTxRoot, block.TxRootBlake3Time = prevEth, prevBlake }()
	defer SetAheadPrepForTest(true)()
	lagPipeRotateToForTest = true
	defer func() { lagPipeRotateToForTest = false }()
	run := func(t *testing.T) (used, grand, hits uint64) {
		u0, g0, h0 := AheadBasePrefillStats()
		testLagPipelineAhead(t, false, arrivalBeforeSnapshot)
		u1, g1, h1 := AheadBasePrefillStats()
		return u1 - u0, g1 - g0, h1 - h0
	}
	t.Run("on", func(t *testing.T) {
		defer SetAheadBasePrefetchForTest(true)()
		used, grand, hits := run(t)
		t.Logf("ahead runs on a prefill %d (grandparent %d), lookups answered %d", used, grand, hits)
		require.GreaterOrEqual(t, used, uint64(8), "the ahead runs did not read through their prefills")
		require.NotZero(t, hits)
	})
	t.Run("grandparent", func(t *testing.T) {
		defer SetAheadBasePrefetchForTest(true)()
		lagPipeAwaitBaseForTest = true
		defer func() { lagPipeAwaitBaseForTest = false }()
		lagPipeAheadClaimedForTest = func(bc *BlockChain, child *block.Block) {
			// Read only once the parent's root applied.
			want := child.Number64().Uint64() - 1
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if st := bc.qmdbRootComputer.AppliedState(); st.Labelled && st.Number >= want {
					return
				}
				time.Sleep(100 * time.Microsecond)
			}
			t.Errorf("the root of %d never applied", want)
		}
		defer func() { lagPipeAheadClaimedForTest = nil }()
		used, grand, hits := run(t)
		t.Logf("ahead runs on a prefill %d (grandparent %d), lookups answered %d", used, grand, hits)
		require.GreaterOrEqual(t, grand, uint64(8), "the prefills were not read on the grandparent's tree")
		require.NotZero(t, hits)
	})
	t.Run("off", func(t *testing.T) {
		defer SetAheadBasePrefetchForTest(false)()
		used, _, _ := run(t)
		require.Zero(t, used)
	})
}
