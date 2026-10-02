package internal

// startupRepairT_test.go covers the startup repair chain invoked from
// BlockChain.Start(): healPlainStateAheadOfMarkerOnStartup,
// verifyAppliedStateOnStartup, alignCanonicalToAppliedOnStartup and
// revertSpeculativeOnStartup all gate on QMDB/marker state that the
// coreT fixture's plain (non-QMDB) chain never sets, so on this fixture they
// exercise their early-return guards; repairCanonicalLinkageOnStartup has no
// such gate and is exercised against a real post-insert canonical chain,
// including the stale-HeadHeaderHash healing branch. BlockChain.Start/Close
// and one manual updateFutureBlocksLoop iteration are covered against a
// fresh, non-shared fixture instance so the background goroutines don't leak
// into other tests.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestStartupRepairFunctionsNoopOnPlainChain(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain
	require.False(t, bc.qmdbEnabled)

	// All four gate on qmdbEnabled/qmdbRootComputer or the QMDBApplied
	// marker, neither of which this fixture sets: every call below must be a
	// pure no-op, never panicking and never mutating chain state.
	require.NotPanics(t, bc.healPlainStateAheadOfMarkerOnStartup)
	require.NotPanics(t, bc.verifyAppliedStateOnStartup)
	require.NotPanics(t, bc.alignCanonicalToAppliedOnStartup)
	require.NotPanics(t, bc.revertSpeculativeOnStartup)

	// Chain head is unaffected.
	head := bc.CurrentBlock()
	require.NotNil(t, head)
	require.Equal(t, uint64(len(fx.Blocks)), head.Number64().Uint64())
}

func TestRepairCanonicalLinkageOnStartupHealthyChainNoop(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	require.NotPanics(t, bc.repairCanonicalLinkageOnStartup)

	// The real chain the fixture built has a consistent canonical mapping
	// (every block came through real InsertChain), so nothing should have
	// been relinked and the head pointer is unchanged.
	head2 := bc.CurrentBlock()
	require.NotNil(t, head2)
	require.Equal(t, uint64(len(fx.Blocks)), head2.Number64().Uint64())
}

func TestRepairCanonicalLinkageOnStartupHealsStaleHeaderHeadMarker(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	// Desync HeadHeaderHash from HeadBlockHash the way an older
	// leader-driven database could leave it, then confirm the repair
	// function advances it back to the committed head.
	require.NoError(t, fx.DB.Update(context.Background(), func(tx kv.RwTx) error {
		headHash := rawdb.ReadHeadBlockHash(tx)
		require.NotEqual(t, types.Hash{}, headHash)
		return rawdb.WriteHeadHeaderHash(tx, types.Hash{0xde, 0xad, 0xbe, 0xef})
	}))

	require.NotPanics(t, bc.repairCanonicalLinkageOnStartup)

	require.NoError(t, fx.DB.View(context.Background(), func(tx kv.Tx) error {
		headHash := rawdb.ReadHeadBlockHash(tx)
		require.Equal(t, headHash, rawdb.ReadHeadHeaderHash(tx))
		return nil
	}))
}

func TestBlockChainStartAndCloseOnQuietFixture(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	require.NoError(t, bc.Start())
	t.Cleanup(func() {
		require.NoError(t, bc.Close())
	})

	// Give the two background goroutines (runLoop, updateFutureBlocksLoop)
	// a moment to reach their select statements before Close() cancels the
	// context; well under the 100ms sleep ceiling.
	time.Sleep(20 * time.Millisecond)

	head := bc.CurrentBlock()
	require.NotNil(t, head)
}

func TestUpdateFutureBlocksLoopProcessesEmptyQueueDirectly(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	// processFutureBlocks is what each ticker iteration of
	// updateFutureBlocksLoop invokes; calling it directly exercises the same
	// logic without paying the loop's 2-second ticker period.
	require.NotPanics(t, bc.processFutureBlocks)
}
