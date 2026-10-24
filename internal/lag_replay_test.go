// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// s132Restart builds the S132 B3 state on a depth-2 lag chain: blocks 1..3
// imported through the lag executor, 1..2 committed, 3 speculative (applied,
// uncommitted -- 14152208 in the round). The node restarts and the startup
// revert unwinds block 3's state while its header and body stay stored. It
// returns the restarted chain, the blocks and block 3's executed root from
// the first run.
func s132Restart(t *testing.T) (*lagChain, []*block.Block, types.Hash) {
	t.Helper()
	lc := newLagChainEngine(t, nil, importBenchEngine{})
	a1 := lc.addrs[1]
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	for n := uint64(1); n <= 3; n++ {
		if n >= 3 {
			waitLagWritten(t, lc.bc, n-2)
		}
		b := lc.build(parent, 0, lc.transfer(0, n-1, a1, 1000+n))
		lc.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
	}
	waitLagWritten(t, lc.bc, 3)
	lc.bc.lock.Lock()
	require.NoError(t, lc.bc.joinLagWrite())
	lc.bc.lock.Unlock()
	require.NoError(t, lc.bc.CommitToCanonical(blks[0].Hash()))
	require.NoError(t, lc.bc.CommitToCanonical(blks[1].Hash()))
	root3 := lc.result(blks[2].Hash()).Root
	require.NoError(t, lc.bc.Close())

	re := reopenLagChain(t, lc)
	re.bc.revertSpeculativeOnStartup()
	require.NoError(t, re.db.View(context.Background(), func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, uint64(2), an, "the startup revert unwinds the speculative block")
		require.Equal(t, blks[1].Hash(), types.Hash(ah))
		require.NotNil(t, rawdb.ReadHeader(tx, blks[2].Hash(), 3), "the reverted block's header is kept")
		require.NotNil(t, rawdb.ReadBlock(tx, blks[2].Hash(), 3), "the reverted block's body is kept")
		return nil
	}))
	require.True(t, re.bc.blockStored(blks[2].Hash(), 3))
	require.False(t, re.bc.lagStoredApplied(blks[2].Hash(), 3))
	return re, blks, root3
}

// TestS132LagExecutorReplaysRevertedParent: after the S132 restart the next
// leader extends the reverted block 3 (it carries the highest QC) with block
// 4. The lag executor must execute block 3 again from its stored body and
// then block 4; before the fix block 4 stayed "not importable yet" forever
// and the leader waited for its lagged execution until every view timed out.
func TestS132LagExecutorReplaysRevertedParent(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	re, blks, root3 := s132Restart(t)
	a1 := re.addrs[1]

	// Block 4 reports block 2's result, which survived the revert: a
	// lag-mode leader can build it, as node1 did in the round.
	b4 := re.build(blks[2].Header().(*block.Header), 0, re.transfer(0, 3, a1, 4))
	re.bc.EnqueueLagImport(b4)
	waitLagWritten(t, re.bc, 4)
	re.bc.lock.Lock()
	require.NoError(t, re.bc.joinLagWrite())
	re.bc.lock.Unlock()
	require.True(t, re.bc.AppliedHeadIsExactly(b4.Hash(), 4))
	require.Equal(t, root3, re.result(blks[2].Hash()).Root, "replayed block 3 reaches its first-run root")
	// And the chain goes on: block 5 reports block 3.
	b5 := re.build(b4.Header().(*block.Header), 0, re.transfer(0, 4, a1, 5))
	require.Equal(t, root3, b5.Header().(*block.Header).Root)
	re.bc.EnqueueLagImport(b5)
	waitLagWritten(t, re.bc, 5)
}

// TestS132StoredUnappliedBlockRequeuedExecutes: the reverted block itself
// pushed again (a peer re-sends the QC'd block) is executed, not dropped as
// "written by another path" because its header is stored.
func TestS132StoredUnappliedBlockRequeuedExecutes(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	re, blks, root3 := s132Restart(t)
	re.bc.EnqueueLagImport(blks[2])
	waitLagWritten(t, re.bc, 3)
	re.bc.lock.Lock()
	require.NoError(t, re.bc.joinLagWrite())
	re.bc.lock.Unlock()
	require.True(t, re.bc.AppliedHeadIsExactly(blks[2].Hash(), 3))
	require.Equal(t, root3, re.result(blks[2].Hash()).Root)
	require.False(t, re.bc.LagImportQueued(blks[2].Hash()))
}

// TestLagStoredUnappliedAncestorsStopsOffTheAppliedBranch: only stored
// blocks that descend from the applied head are replayed; a parent on a
// sibling branch or not stored at all is left to the authorized import and
// to catch-up.
func TestLagStoredUnappliedAncestorsStopsOffTheAppliedBranch(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	re, blks, _ := s132Restart(t)
	b4 := re.build(blks[2].Header().(*block.Header), 0)
	chain := re.bc.lagStoredUnappliedAncestors(b4)
	require.Len(t, chain, 1)
	require.Equal(t, blks[2].Hash(), chain[0].Hash())
	// A child of the applied head needs nothing.
	c3 := re.build(blks[1].Header().(*block.Header), 0xC)
	require.Empty(t, re.bc.lagStoredUnappliedAncestors(c3))
	// A parent that is not stored.
	c4 := re.build(c3.Header().(*block.Header), 0xC)
	require.Empty(t, re.bc.lagStoredUnappliedAncestors(c4))
	// A stored parent on a sibling branch below the applied head.
	sib := re.build(blks[0].Header().(*block.Header), 0xD)
	re.bc.forgetAvailable(sib.Hash())
	require.NoError(t, re.db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, sib.Header().(*block.Header))
		return nil
	}))
	sibChild := re.build(sib.Header().(*block.Header), 0xD)
	require.Empty(t, re.bc.lagStoredUnappliedAncestors(sibChild))
}
