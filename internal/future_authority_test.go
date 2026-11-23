package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// futureAuthOrphan builds a block at head+1 whose parent this chain has never
// seen, so every import path future-queues it.
func futureAuthOrphan(t *testing.T, bc *BlockChain, salt byte) *block.Block {
	t.Helper()
	head := bc.CurrentBlock()
	hh, ok := head.Header().(*block.Header)
	require.True(t, ok)
	h := &block.Header{
		ParentHash: types.BytesToHash([]byte{0xde, 0xad, salt}),
		Number:     uint256.NewInt(0).Add(hh.Number, uint256.NewInt(1)),
		GasLimit:   hh.GasLimit,
		Time:       hh.Time + 1,
		Coinbase:   types.BytesToAddress([]byte{salt}),
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       hh.Root,
	}
	return block.NewBlockFromReceipt(h, nil, nil, nil, nil).(*block.Block)
}

func futureQueued(bc *BlockChain, h types.Hash) bool {
	_, ok := bc.futureBlocks.Get(h)
	return ok
}

// Blocks a passive import queues (push, gossip, InsertChain) and blocks a
// range catch-up queues are retried WITHOUT branch-switch authority; only a
// consensus-driven import (InsertChainAuthorized) queues with it.
func TestFutureQueueAuthorityFollowsProvenance(t *testing.T) {
	bc := coreTNewChainFixture(t).Chain

	passive := futureAuthOrphan(t, bc, 1)
	_, _ = bc.InsertChain([]block.IBlock{passive})
	require.True(t, futureQueued(bc, passive.Hash()))
	require.False(t, bc.futureRetryAuthorized(passive.Hash()), "passive import must not queue with authority")

	pushed := futureAuthOrphan(t, bc, 2)
	require.NoError(t, bc.AddFutureBlock(pushed)) // push/gossip ingress
	require.False(t, bc.futureRetryAuthorized(pushed.Hash()), "pushed block must not gain authority in the queue")

	catchUp := futureAuthOrphan(t, bc, 3)
	_, _ = bc.InsertChainCatchUp([]block.IBlock{catchUp})
	require.True(t, futureQueued(bc, catchUp.Hash()))
	require.False(t, bc.futureRetryAuthorized(catchUp.Hash()), "range catch-up data must not queue with authority")
	require.False(t, bc.futureQueuePassive, "catch-up flag must be cleared after the call")

	named := futureAuthOrphan(t, bc, 4)
	_, _ = bc.InsertChainAuthorized([]block.IBlock{named})
	require.True(t, futureQueued(bc, named.Hash()))
	require.True(t, bc.futureRetryAuthorized(named.Hash()), "consensus-named import keeps its authority")

	// A later consensus-named import upgrades a passive entry; a later
	// passive add never downgrades.
	_, _ = bc.InsertChainAuthorized([]block.IBlock{passive})
	require.True(t, bc.futureRetryAuthorized(passive.Hash()))
	require.NoError(t, bc.AddFutureBlock(named))
	require.True(t, bc.futureRetryAuthorized(named.Hash()))
}

func TestFutureQueueAuthorityLegacySwitch(t *testing.T) {
	defer synclegacy.FutureAuthority.SetForTest(true)()
	bc := coreTNewChainFixture(t).Chain
	pushed := futureAuthOrphan(t, bc, 5)
	require.NoError(t, bc.AddFutureBlock(pushed))
	require.True(t, bc.futureRetryAuthorized(pushed.Hash()), "legacy retries every queued block with authority")
	catchUp := futureAuthOrphan(t, bc, 6)
	_, _ = bc.InsertChainCatchUp([]block.IBlock{catchUp})
	require.True(t, bc.futureRetryAuthorized(catchUp.Hash()))
}

// A retried block that imports leaves both the queue and the authority set.
func TestFutureQueueAuthorityDroppedOnImport(t *testing.T) {
	bc := coreTNewChainFixture(t).Chain
	hh, ok := bc.CurrentBlock().Header().(*block.Header)
	require.True(t, ok)
	next := coreTBuildEmptyChildBlock(hh, 10)
	bc.futureBlocks.Add(next.Hash(), next)
	bc.futureAuth.mark(next.Hash(), bc.futureBlocks.Contains)
	bc.processFutureBlocks()
	require.False(t, futureQueued(bc, next.Hash()))
	require.False(t, bc.futureAuth.has(next.Hash()))
	require.Equal(t, next.Hash(), bc.CurrentBlock().Hash())
}

// The authority set stays bounded.
func TestFutureQueueAuthorityBounded(t *testing.T) {
	var fa futureAuthority
	none := func(types.Hash) bool { return false }
	for i := 0; i < 10*maxFutureBlocks; i++ {
		fa.mark(types.BytesToHash([]byte{byte(i), byte(i >> 8), 7}), none)
	}
	require.LessOrEqual(t, len(fa.m), 4*maxFutureBlocks)
}
