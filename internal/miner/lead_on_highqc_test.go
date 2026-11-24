// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

// lead_on_highqc_test.go: a leader must be able to build on its highQC block
// when the applied head is an uncertified child of it (mixed round 2,
// 2026-11-24: block 1 was imported and applied, refused at the vote gate,
// never certified; the next gov5 leader, whose highQC was the genesis QC,
// failed every build with "parent is not the applied head and its build is
// not on the tree" and the chain never left height 0).

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// leadHSEngine is the fixture engine reporting HotStuff, so imports do not
// move the canonical head: only CommitToCanonical does, as on a live chain.
type leadHSEngine struct{ mnTFakerEngine }

func (leadHSEngine) Type() params.ConsensusType { return params.HotStuffConsensus }

// Finalize checks the state root on import as the HotStuff engine does: the
// root is computed on the chain's live tree, which is what applies the block.
func (leadHSEngine) Finalize(_ consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	h := header.(*block.Header)
	if root := ibs.IntermediateRoot(); root != h.Root {
		return nil, nil, fmt.Errorf("state root mismatch: header %x, computed %x", h.Root, root)
	}
	return nil, nil, nil
}

// leadNewQMDBFixture is a HotStuff-shaped chain committing with QMDB: the
// genesis is seeded into the twig forest and the live tree is loaded from
// the store as node start-up does.
func leadNewQMDBFixture(t *testing.T) *mnTChainFixture {
	t.Helper()
	f := mnTNewChainFixtureWithEngine(t, leadHSEngine{}, func(cfg *params.ChainConfig, _ conf.GenesisAlloc) {
		cfg.StateScheme = "qmdb"
	})
	leadInstallQMDB(t, f)
	return f
}

// leadInstallQMDB loads the live tree from the store and pre-warms the
// miner's speculative tree, as node start-up does.
func leadInstallQMDB(t *testing.T, f *mnTChainFixture) {
	t.Helper()
	rc := commitment.NewQMDBRootComputer()
	require.NoError(t, f.DB.View(context.Background(), func(tx kv.Tx) error {
		rc.SetCold(tx)
		defer rc.SetCold(nil)
		return rc.LoadFrom(tx)
	}))
	rc.EnableUndoRecording()
	f.Chain.SetQMDBRootComputer(rc)
	require.NoError(t, f.DB.View(context.Background(), func(tx kv.Tx) error {
		require.True(t, f.Chain.PrewarmMinerRootComputer(tx))
		return nil
	}))
}

// leadBuild runs one non-speculative leader build on parent (zero: the local
// head, which is what HotStuff passes for the genesis QC) and returns the
// block handed to the sealer.
func leadBuild(t *testing.T, f *mnTChainFixture, w *worker, parent types.Hash) (block.IBlock, error) {
	t.Helper()
	if err := w.commitWork(nil, false, time.Now().Unix(), parent, false, time.Time{}); err != nil {
		return nil, err
	}
	select {
	case tk := <-w.taskCh:
		require.NotNil(t, tk.block)
		return tk.block, nil
	default:
		t.Fatal("commitWork returned nil without sending a task")
		return nil, nil
	}
}

func leadWorker(t *testing.T, f *mnTChainFixture, pool *mnTStubTxsPool) *worker {
	w := mnTBareWorker(t, f, pool)
	w.coinbase = f.Coinbase
	w.resubmitAdjustCh = make(chan *intervalAdjust, 10)
	atomic.StoreInt32(&w.running, 1)
	return w
}

// TestLeaderBuildsOnGenesisOverUncertifiedAppliedChild: block 1 is imported
// and applied but never committed (the canonical head stays genesis); the
// leader of the next view holds the genesis QC and must still produce a
// valid block on genesis.
func TestLeaderBuildsOnGenesisOverUncertifiedAppliedChild(t *testing.T) {
	f := leadNewQMDBFixture(t)
	genesis := f.Chain.CurrentBlock()

	// Another leader's block 1, imported and applied, not certified.
	other := leadWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0],
		f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000)))})
	b1a, err := leadBuild(t, f, other, types.Hash{})
	require.NoError(t, err)
	_, err = f.Chain.InsertChain([]block.IBlock{b1a})
	require.NoError(t, err)
	require.True(t, f.Chain.AppliedHeadIsExactly(b1a.Hash(), 1), "block 1 must be the applied head")
	require.Equal(t, genesis.Hash(), f.Chain.CurrentBlock().Hash(), "an uncommitted import must not move the canonical head")

	// This node leads with highQC = genesis QC (zero hash).
	leader := leadWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[2],
		f.mnTSignedTransfer(t, 2, f.Senders[3], uint256.NewInt(2_000)))})
	b1b, err := leadBuild(t, f, leader, types.Hash{})
	require.NoError(t, err, "the leader must build on genesis over an uncertified applied child")
	require.Equal(t, genesis.Hash(), b1b.ParentHash())
	require.NotEqual(t, b1a.Hash(), b1b.Hash())
	require.Len(t, b1b.Transactions(), 1)
	require.True(t, f.Chain.AppliedHeadIsExactly(genesis.Hash(), 0), "the build must have unwound the uncertified child")

	// The proposal is valid: an importer reproduces its root.
	_, err = f.Chain.InsertChainAuthorized([]block.IBlock{b1b})
	require.NoError(t, err)
	require.True(t, f.Chain.AppliedHeadIsExactly(b1b.Hash(), 1))
}

// TestLeaderBuildsOnCommittedHeadOverUncertifiedAppliedChild is the same at
// height 1: block 1 is committed, an uncertified block 2 is applied on it,
// and the leader holding block 1's QC builds a valid block 2 on block 1,
// both when consensus names the parent and when it passes the zero hash.
func TestLeaderBuildsOnCommittedHeadOverUncertifiedAppliedChild(t *testing.T) {
	for _, tc := range []struct {
		name       string
		namedQCBlk bool
	}{{"named-parent", true}, {"zero-parent", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := leadNewQMDBFixture(t)
			w0 := leadWorker(t, f, &mnTStubTxsPool{})
			b1, err := leadBuild(t, f, w0, types.Hash{})
			require.NoError(t, err)
			_, err = f.Chain.InsertChain([]block.IBlock{b1})
			require.NoError(t, err)
			require.NoError(t, f.Chain.CommitToCanonical(b1.Hash()))
			require.Equal(t, b1.Hash(), f.Chain.CurrentBlock().Hash())

			other := leadWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0],
				f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000)))})
			b2a, err := leadBuild(t, f, other, b1.Hash())
			require.NoError(t, err)
			_, err = f.Chain.InsertChain([]block.IBlock{b2a})
			require.NoError(t, err)
			require.True(t, f.Chain.AppliedHeadIsExactly(b2a.Hash(), 2))
			require.Equal(t, b1.Hash(), f.Chain.CurrentBlock().Hash())

			parent := types.Hash{}
			if tc.namedQCBlk {
				parent = b1.Hash()
			}
			leader := leadWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[2],
				f.mnTSignedTransfer(t, 2, f.Senders[3], uint256.NewInt(2_000)))})
			b2b, err := leadBuild(t, f, leader, parent)
			require.NoError(t, err)
			require.Equal(t, b1.Hash(), b2b.ParentHash())
			require.Len(t, b2b.Transactions(), 1)
			_, err = f.Chain.InsertChainAuthorized([]block.IBlock{b2b})
			require.NoError(t, err)
			require.True(t, f.Chain.AppliedHeadIsExactly(b2b.Hash(), 2))
		})
	}
}
