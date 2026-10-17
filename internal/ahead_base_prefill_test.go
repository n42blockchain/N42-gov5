// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// lagPipeAwaitBaseForTest makes the beforeSnapshot harness (with
// lagPipeAwaitPrepForTest) hold each parent's snapshot until its child's
// base prefill finished: it read the tree before the parent's root.
var lagPipeAwaitBaseForTest bool

// lagPipeRotateToForTest makes the pipeline harness send block n's
// transfers to one of three recipients, n mod 3: each child credits an
// account its parent did not write.
var lagPipeRotateToForTest bool

// lagPipeAwaitGrandBaseForTest makes the beforeSnapshot harness hold each
// parent's snapshot until its grandchild's base prefill finished: it read
// the tree two blocks below its own parent.
var lagPipeAwaitGrandBaseForTest bool

// lagPipeDeferDepthForTest, when positive, is the harness's deferred depth
// (default 2): at 3 a grandchild is available before its grandparent's
// root.
var lagPipeDeferDepthForTest int

// lagPipeRotateModForTest, when positive, is the recipient rotation of
// lagPipeRotateToForTest (default 3): at 2 each block credits the account
// its grandparent credited and its parent did not.
var lagPipeRotateModForTest int

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
//   - on: the default arrival before the snapshot, each claim waiting for
//     its fill;
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
		// A claim that comes first would stop the fills before they read
		// (the race detector's pace): let them finish.
		defer SetAheadBaseClaimWaitForTest(true)()
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
	// deep (P15b): at deferred depth 3 each grandchild's prefill reads the
	// tree before its grandparent's root -- two blocks below its parent --
	// and every block credits the recipient its grandparent credited: the
	// value must come from the grandparent's post-state layered under the
	// parent's, not from the prefill.
	deep := func(t *testing.T, k int) (used, deepUsed uint64) {
		defer SetAheadBasePrefetchForTest(true)()
		defer SetAheadBaseDepthForTest(k)()
		lagPipeDeferDepthForTest, lagPipeRotateModForTest, lagPipeAwaitGrandBaseForTest = 3, 2, true
		defer func() { lagPipeDeferDepthForTest, lagPipeRotateModForTest, lagPipeAwaitGrandBaseForTest = 0, 0, false }()
		d0 := AheadBaseDeepStats()
		used, _, _ = run(t)
		return used, AheadBaseDeepStats() - d0
	}
	t.Run("deep", func(t *testing.T) {
		used, deepUsed := deep(t, 2)
		t.Logf("ahead runs on a prefill %d, two blocks below the parent %d", used, deepUsed)
		require.GreaterOrEqual(t, deepUsed, uint64(6), "the prefills were not read below the grandparent")
	})
	t.Run("deepDepth1", func(t *testing.T) {
		// K = 1: no prefill reads below the grandparent (each waits for the
		// held tree until it gives up).
		prev := aheadBaseMaxWait
		aheadBaseMaxWait = 50 * time.Millisecond
		defer func() { aheadBaseMaxWait = prev }()
		_, deepUsed := deep(t, 1)
		require.Zero(t, deepUsed)
	})
	t.Run("deepMissingLayer", func(t *testing.T) {
		// The grandparent's post-state is gone when the run looks for it:
		// the prefill is dropped, the run reads per lookup.
		lagPipeAheadLayersForTest = func(p *lagPipeline) {
			p.mu.Lock()
			p.older = nil
			p.mu.Unlock()
		}
		defer func() { lagPipeAheadLayersForTest = nil }()
		_, deepUsed := deep(t, 2)
		require.Zero(t, deepUsed)
	})
}

// TestAheadBaseLayers (P15b): a prefill read below the parent's parent gets
// the kept post-states of every block down to its height, newest first,
// or is dropped when one is missing; one read on the parent or the
// grandparent keeps the run's layers.
func TestAheadBaseLayers(t *testing.T) {
	defer SetAheadBaseDepthForTest(3)()
	h := func(n uint64) types.Hash { return types.Hash{0xd0, byte(n)} }
	snap := func(n uint64) *lagPipeParent {
		hdr := &block.Header{Number: uint256.NewInt(n), ParentHash: h(n - 1)}
		return &lagPipeParent{hash: h(n), number: n, header: hdr, post: &state.PostState{}, written: true}
	}
	p := &lagPipeline{}
	for n := uint64(6); n <= 9; n++ {
		p.mu.Lock()
		p.parent = snap(n)
		p.mu.Unlock()
		p.mu.Lock()
		p.dropParentLocked()
		p.mu.Unlock()
	}
	// Kept: 9 and 8 (depth 3 keeps two).
	require.Len(t, p.older, 2)
	require.Equal(t, h(9), p.older[0].hash)
	require.Equal(t, h(8), p.older[1].hash)
	par := snap(10)
	base := []*state.PostState{par.post}
	pending := append(base, p.older[0].post)

	ab := &aheadBase{pf: &commitment.BasePrefill{}, at: 8, atHash: h(8)}
	got := p.aheadBaseLayers(ab, par, base)
	require.NotNil(t, ab.pf)
	require.Equal(t, []*state.PostState{par.post, p.older[0].post}, got)

	ab = &aheadBase{pf: &commitment.BasePrefill{}, at: 7, atHash: h(7)}
	got = p.aheadBaseLayers(ab, par, pending)
	require.NotNil(t, ab.pf)
	require.Equal(t, []*state.PostState{par.post, p.older[0].post, p.older[1].post}, got)

	// The grandparent: the run's own layers.
	ab = &aheadBase{pf: &commitment.BasePrefill{}, at: 9, atHash: h(9)}
	require.Equal(t, pending, p.aheadBaseLayers(ab, par, pending))
	require.NotNil(t, ab.pf)

	// Another block at the start height, a height nothing is kept for, a
	// missing ancestor: dropped, layers unchanged.
	for _, tc := range []struct {
		at   uint64
		hash types.Hash
	}{{9, types.Hash{0xee}}, {7, types.Hash{0xee}}, {6, h(6)}} {
		ab = &aheadBase{pf: &commitment.BasePrefill{}, at: tc.at, atHash: tc.hash}
		require.Equal(t, base, p.aheadBaseLayers(ab, par, base))
		require.Nil(t, ab.pf, "at %d", tc.at)
	}
	p.mu.Lock()
	p.forgetOlderLocked(h(9))
	p.mu.Unlock()
	ab = &aheadBase{pf: &commitment.BasePrefill{}, at: 8, atHash: h(8)}
	p.aheadBaseLayers(ab, par, base)
	require.Nil(t, ab.pf, "the parent's parent forgotten")
}

// TestAheadBaseChainMoves: the tree states a prefill may start on and the
// mutations it tolerates while it reads.
func TestAheadBaseChainMoves(t *testing.T) {
	h := func(n uint64) types.Hash { return types.Hash{0xd1, byte(n)} }
	c := aheadBaseChain{top: 10, hash: []types.Hash{h(10), h(9), h(8)}}
	lab := func(gen, n uint64, hash types.Hash) commitment.QMDBAppliedState {
		return commitment.QMDBAppliedState{Gen: gen, Labelled: true, Number: n, Hash: hash}
	}
	for _, tc := range []struct {
		st commitment.QMDBAppliedState
		d  uint64
		ok bool
	}{
		{lab(1, 10, h(10)), 0, true}, {lab(1, 9, h(9)), 1, true}, {lab(1, 8, h(8)), 2, true},
		{lab(1, 7, h(7)), 0, false}, {lab(1, 11, h(11)), 0, false}, {lab(1, 9, types.Hash{1}), 0, false},
		{commitment.QMDBAppliedState{Gen: 1}, 0, false},
	} {
		d, ok := c.holds(tc.st)
		require.Equal(t, tc.ok, ok, "%+v", tc.st)
		if ok {
			require.Equal(t, tc.d, d)
		}
	}
	require.True(t, aheadBaseMovedOK(c, 8, nil))
	require.True(t, aheadBaseMovedOK(c, 8, []commitment.QMDBAppliedState{lab(2, 9, h(9)), lab(3, 10, h(10))}))
	require.True(t, aheadBaseMovedOK(c, 9, []commitment.QMDBAppliedState{lab(2, 10, h(10))}))
	require.False(t, aheadBaseMovedOK(c, 8, []commitment.QMDBAppliedState{lab(2, 10, h(10))}), "a skipped root")
	require.False(t, aheadBaseMovedOK(c, 8, []commitment.QMDBAppliedState{lab(2, 9, types.Hash{1})}), "a sibling's root")
	require.False(t, aheadBaseMovedOK(c, 8, []commitment.QMDBAppliedState{{Gen: 2}}), "an unlabelled mutation")
	require.False(t, aheadBaseMovedOK(c, 10, []commitment.QMDBAppliedState{lab(2, 11, h(11))}), "past the parent")
	require.Equal(t, 1, parseAheadBaseDepth(""))
	require.Equal(t, 3, parseAheadBaseDepth("3"))
	require.Equal(t, aheadBaseMaxDepth, parseAheadBaseDepth("9"))
	require.Equal(t, 1, parseAheadBaseDepth("0"))
}
