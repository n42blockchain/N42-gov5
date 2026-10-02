package internal

// minerRewindMiscT_test.go covers a handful of small, previously-0%-covered
// BlockChain helpers that are cheap to exercise directly against the coreT
// fixture: the miner speculative-tree rewind queue (queueMinerRewind /
// applyMinerRewindsLocked) on a chain with no miner root computer yet,
// PrewarmMinerRootComputer's qmdb-disabled guard, ReadConsensusEvidence's
// not-found path, and the syncChain stub.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
)

func TestQueueMinerRewindNilAndNoComputer(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	// nil undo: documented no-op.
	require.NotPanics(t, func() { bc.queueMinerRewind(nil) })

	// minerRC is nil on a freshly built fixture chain (no speculative build
	// has run yet): queueMinerRewind must still be a safe no-op rather than
	// queueing an undo nothing will ever consume.
	require.NotPanics(t, func() { bc.queueMinerRewind(&qmdb.BlockUndo{}) })
	require.Empty(t, bc.minerPendingUndo)
}

func TestApplyMinerRewindsLockedEmptyPending(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	require.NoError(t, fx.DB.View(context.Background(), func(tx kv.Tx) error {
		require.NotPanics(t, func() { bc.applyMinerRewindsLocked(nil, tx) })
		return nil
	}))
}

func TestPrewarmMinerRootComputerDisabledWhenQMDBOff(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain
	require.False(t, bc.qmdbEnabled)

	require.NoError(t, fx.DB.View(context.Background(), func(tx kv.Tx) error {
		require.False(t, bc.PrewarmMinerRootComputer(tx))
		return nil
	}))
}

func TestReadConsensusEvidenceNotFound(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	ev, err := bc.ReadConsensusEvidence(999999)
	require.NoError(t, err)
	require.Nil(t, ev)
}

func TestSyncChainIsANoopStub(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	require.NotPanics(t, func() { bc.syncChain(1, peer.ID("")) })
}
