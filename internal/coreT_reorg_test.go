package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
)

// coreTBuildEmptyChildBlock builds a transaction-less child of parent. With
// no transactions the state is untouched, so the child's Root is exactly the
// parent's Root -- this lets a test construct a multi-block alternate branch
// without needing to replay execution against a state the single-state
// backend doesn't retain for non-head ancestors.
func coreTBuildEmptyChildBlock(parentHeader *block.Header, timeOffset uint64) *block.Block {
	header := &block.Header{
		ParentHash: parentHeader.Hash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + timeOffset,
		Coinbase:   parentHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       parentHeader.Root,
		GasUsed:    0,
	}
	return block.NewBlockFromReceipt(header, nil, nil, nil, nil).(*block.Block)
}

// TestCoreTInsertSideChainCannotCompleteReorg builds a longer, empty-block
// alternate branch forking from an ancestor two blocks behind the head and
// inserts it in one InsertChain call. The alternate branch's blocks don't
// touch state (no transactions), so their Root/TxHash/ReceiptHash are
// trivially valid against an unchanged state -- which is meant to let a
// test build a multi-block side branch without needing per-ancestor state
// snapshots this PlainState-backed chain doesn't keep.
//
// DEFECT (documented, not fixed per task instructions, root cause not fully
// isolated -- see below): the reorg never completes. InsertChain returns a
// bare "pruned ancestor" error (ErrPrunedAncestor, unwrapped) for this
// strictly-longer, structurally valid side chain, and the canonical head
// stays on the old (shorter) branch. The side blocks ARE nonetheless
// persisted (retrievable by hash afterward, asserted below), which is only
// possible through blockchain.go's writeBlockWithTd -- the sole writer of a
// non-canonical block's header/body -- so the import attempt does reach
// insertSideChain's storage loop before failing. (Coverage instrumentation
// on this branch oddly attributes 0% to insertSideChain itself despite this
// side effect, which was not resolved in the time available; the precise
// line that produces the error was not pinned down with certainty.) Net
// effect either way: a strictly-longer competing branch is rejected outright
// instead of triggering a reorg, which is a fork-choice-breaking bug if hit
// live.
func TestCoreTInsertSideChainCannotCompleteReorg(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.GreaterOrEqual(t, len(f.Blocks), 3)

	oldHead := bc.CurrentBlock()
	forkPoint := f.Blocks[len(f.Blocks)-3] // two blocks behind the head
	forkHeader := forkPoint.Header().(*block.Header)

	sib1 := coreTBuildEmptyChildBlock(forkHeader, 5)
	sib2 := coreTBuildEmptyChildBlock(sib1.Header().(*block.Header), 5)
	sib3 := coreTBuildEmptyChildBlock(sib2.Header().(*block.Header), 5)
	sib4 := coreTBuildEmptyChildBlock(sib3.Header().(*block.Header), 5) // strictly longer than oldHead

	_, err := bc.InsertChain([]block.IBlock{sib1, sib2, sib3, sib4})
	require.ErrorIs(t, err, ErrPrunedAncestor, "see DEFECT comment above")

	// The old branch is left untouched by the failed reorg attempt.
	require.Equal(t, oldHead.Hash(), bc.CurrentBlock().Hash())

	// The side blocks were nonetheless stored (insertSideChain's
	// store-without-execution loop ran to completion before the walk-back
	// failed), so they're retrievable by hash even though they never became
	// canonical.
	got, err := bc.GetBlockByHash(sib1.Hash())
	require.NoError(t, err)
	require.NotNil(t, got)
}
