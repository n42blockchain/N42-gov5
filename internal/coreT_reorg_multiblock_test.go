package internal

// coreT_reorg_multiblock_test.go drives reorg() with a TWO-block new side
// chain (not just a single-block sibling), which is the only way to reach
// the "insert new chain blocks (except head)" loop body in reorg() --- with
// a one-block new chain that loop's `i >= 1` bound is never true.

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestCoreTReorg_TwoBlockNewChain(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.GreaterOrEqual(t, len(f.Blocks), 3)

	oldHead := bc.CurrentBlock()

	// Fork point: two blocks behind head.
	forkHeader, ok := f.Blocks[len(f.Blocks)-3].Header().(*block.Header)
	require.True(t, ok)

	sibling1 := coreTBuildEmptyChildBlock(forkHeader, 500)
	sibling1Header, ok := sibling1.Header().(*block.Header)
	require.True(t, ok)
	sibling2 := coreTBuildEmptyChildBlock(sibling1Header, 500)

	require.Equal(t, oldHead.Number64().Uint64(), sibling2.Number64().Uint64())
	require.NotEqual(t, oldHead.Hash(), sibling2.Hash())

	// sibling1 must already be retrievable from storage: reorg()'s
	// common-ancestor walk calls rawdb.ReadBlock on newBlock's parent, not
	// an in-memory map.
	require.NoError(t, bc.writeBlockWithTd(sibling1, uint256.NewInt(1)))

	err := bc.writeKnownBlock(nil, sibling2)
	require.NoError(t, err)

	require.Equal(t, sibling2.Hash(), bc.CurrentBlock().Hash())

	roTx, rerr := f.DB.BeginRo(bc.ctx)
	require.NoError(t, rerr)
	defer roTx.Rollback()

	// Both new-chain blocks became canonical at their heights: sibling1 (via
	// reorg()'s writeHeadBlock loop over newChain[1:]) and sibling2 (via
	// writeKnownBlock's own writeHeadBlock call after reorg returns).
	canon1, cerr := rawdb.ReadCanonicalHash(roTx, sibling1.Number64().Uint64())
	require.NoError(t, cerr)
	require.Equal(t, sibling1.Hash(), canon1)

	canon2, cerr := rawdb.ReadCanonicalHash(roTx, sibling2.Number64().Uint64())
	require.NoError(t, cerr)
	require.Equal(t, sibling2.Hash(), canon2)
}
