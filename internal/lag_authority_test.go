// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// lagAuthorityFixture: canonical g, A1, A2 with A2 applied; a stored sibling
// branch S2 off A1. S3 (on S2) waits in the lag executor: its parent is
// stored but not applied, so importing it needs branch-switch authority.
// Imports are stopped (procInterrupt), so an attempted import returns at
// once and only marks the queue entry (stuckLog).
func newLagAuthorityFixture(t *testing.T) (*BlockChain, *canonFoldFixture, *lagQueued) {
	t.Helper()
	f := newCanonFoldFixture()
	db := newRealignTestDB(t)
	f.seed(t, db, []*block.Block{f.a[0], f.a[1], f.a[2]}, f.a[2], nil)
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), qmdbEnabled: true}
	bc.currentBlock.Store(f.a[1])
	atomic.StoreInt32(&bc.procInterrupt, 1)
	q := &lagQueued{blk: f.s[3], since: time.Now().Add(-10 * time.Second)}
	bc.lagExec.queued = map[types.Hash]*lagQueued{}
	bc.lagExec.queued[f.s[3].Hash()] = q
	return bc, f, q
}

// A block only a peer pushed never switches the applied branch from the
// lag executor; a block a verified proposal or CommitQC named does, as does
// any block under the legacy switch.
func TestLagExecutorSwitchNeedsConsensusName(t *testing.T) {
	t.Run("pushed", func(t *testing.T) {
		bc, f, q := newLagAuthorityFixture(t)
		require.False(t, bc.lagStep())
		require.False(t, q.stuckLog, "a peer-named block was imported with switch authority")
		require.True(t, bc.LagImportQueued(f.s[3].Hash()))
	})
	t.Run("named", func(t *testing.T) {
		bc, f, q := newLagAuthorityFixture(t)
		bc.MarkConsensusNamed(f.s[3].Hash())
		require.False(t, bc.lagStep())
		require.True(t, q.stuckLog, "a consensus-named block was not imported with switch authority")
	})
	t.Run("futureAuthorized", func(t *testing.T) {
		bc, f, q := newLagAuthorityFixture(t)
		bc.futureAuth.mark(f.s[3].Hash(), func(types.Hash) bool { return true })
		require.False(t, bc.lagStep())
		require.True(t, q.stuckLog)
	})
	t.Run("legacy", func(t *testing.T) {
		defer synclegacy.FutureAuthority.SetForTest(true)()
		bc, _, q := newLagAuthorityFixture(t)
		require.False(t, bc.lagStep())
		require.True(t, q.stuckLog)
	})
}

// S1 enforce: a queued block the sync layer holds as an unanchored
// catch-up tail block is not imported by the lag executor, even with its
// parent applied; once released it is.
func TestLagExecutorSkipsAnchorHeldBlock(t *testing.T) {
	bc, f, _ := newLagAuthorityFixture(t)
	// A3 extends the applied head A2: ready, no switch needed.
	q := &lagQueued{blk: f.a[3], since: time.Now().Add(-10 * time.Second)}
	bc.lagExec.queued = map[types.Hash]*lagQueued{f.a[3].Hash(): q}
	held := true
	bc.SetLagHoldProbe(func(h types.Hash) bool { return held && h == f.a[3].Hash() })
	require.False(t, bc.lagStep())
	require.False(t, q.stuckLog, "an anchor-held block was imported")
	held = false
	require.False(t, bc.lagStep())
	require.True(t, q.stuckLog, "a released block was not imported")
}

// S5 Range (N42_SYNC_PIPELINE): the lag executor imports without
// InsertChain, so a queued block at a height the sync importer owns (the
// future insert gate) stays queued even with its parent applied; once the
// gate hands the height back it is imported.
func TestLagExecutorLeavesSyncOwnedHeight(t *testing.T) {
	bc, f, _ := newLagAuthorityFixture(t)
	q := &lagQueued{blk: f.a[3], since: time.Now().Add(-10 * time.Second)}
	bc.lagExec.queued = map[types.Hash]*lagQueued{f.a[3].Hash(): q}
	owned := true
	n3 := f.a[3].Number64().Uint64()
	bc.SetFutureInsertGate(func(n uint64) bool { return owned && n <= n3 })
	require.False(t, bc.lagStep())
	require.False(t, q.stuckLog, "a sync-owned height was imported by the lag executor")
	require.Equal(t, "sync-range", bc.lagExec.idle.skipWhy)
	require.True(t, bc.LagImportQueued(f.a[3].Hash()), "a sync-owned block left the lag queue")
	owned = false
	require.False(t, bc.lagStep())
	require.True(t, q.stuckLog, "a height handed back to the live path was not imported")
}
