package internal

// coreT_reorg_sibling_test.go drives a REAL one-block reorg (not the no-op
// same-hash short circuit TestCoreTWriteKnownBlockSameHead exercises): a
// transaction-less sibling of the current head, forking from the head's
// parent, is written through writeKnownBlock. Because the sibling has no
// transactions its Root/TxHash/ReceiptHash equal the parent's, so it is
// valid against the single-state PlainState backend without needing a
// snapshot of a non-head ancestor's state (same trick as
// coreT_reorg_test.go's coreTBuildEmptyChildBlock helper). This walks
// reorg()'s real common-ancestor search, canonical-hash truncation and
// writeKnownBlock's reorg branch, rather than the identical-head fast path.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestCoreTWriteKnownBlockSiblingReorg(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.GreaterOrEqual(t, len(f.Blocks), 2)

	oldHead := bc.CurrentBlock()
	oldHeadHeader, ok := oldHead.Header().(*block.Header)
	require.True(t, ok)

	// Fork from the head's own parent (one block back), building a sibling
	// of oldHead with a different timestamp (-> different hash, same Root).
	parentHeader, ok := f.Blocks[len(f.Blocks)-2].Header().(*block.Header)
	require.True(t, ok)
	require.Equal(t, parentHeader.Hash(), oldHeadHeader.ParentHash)

	sibling := coreTBuildEmptyChildBlock(parentHeader, 999)
	require.NotEqual(t, oldHead.Hash(), sibling.Hash())
	siblingNumber := sibling.Header().(*block.Header).Number.Uint64()
	require.Equal(t, oldHeadHeader.Number.Uint64(), siblingNumber)

	err := bc.writeKnownBlock(nil, sibling)
	require.NoError(t, err)

	// The sibling is now canonical head...
	require.Equal(t, sibling.Hash(), bc.CurrentBlock().Hash())

	// ...and the canonical-hash marker at this height points at the
	// sibling, not the old head: reorg() replaced it rather than merely
	// extending the chain.
	roTx, rerr := f.DB.BeginRo(bc.ctx)
	require.NoError(t, rerr)
	defer roTx.Rollback()
	canonicalHash, cerr := rawdb.ReadCanonicalHash(roTx, siblingNumber)
	require.NoError(t, cerr)
	require.Equal(t, sibling.Hash(), canonicalHash)
	require.NotEqual(t, oldHead.Hash(), canonicalHash)
}
