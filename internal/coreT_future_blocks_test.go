package internal

// coreT_future_blocks_test.go covers processFutureBlocks: the empty-queue
// short circuit, the "still too far ahead" gate (block stays queued), and
// the success path (the next block is inserted and leaves the queue).

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
)

func TestCoreTProcessFutureBlocks_EmptyQueue(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.Equal(t, 0, bc.futureBlocks.Len())
	bc.processFutureBlocks() // must be a no-op; no panic, queue stays empty
	require.Equal(t, 0, bc.futureBlocks.Len())
}

func TestCoreTProcessFutureBlocks_TooFarAhead(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	head := bc.CurrentBlock()
	headHeader, ok := head.Header().(*block.Header)
	require.True(t, ok)

	// A block far beyond head+1: processFutureBlocks must leave it queued.
	farHeader := &block.Header{
		ParentHash: head.Hash(), // doesn't need to resolve; rejected by the number gate first
		Number:     uint256.NewInt(0).Add(headHeader.Number, uint256.NewInt(100)),
		GasLimit:   headHeader.GasLimit,
		Time:       headHeader.Time + 1000,
		Coinbase:   headHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       headHeader.Root,
	}
	farBlock := block.NewBlockFromReceipt(farHeader, nil, nil, nil, nil).(*block.Block)
	bc.futureBlocks.Add(farBlock.Hash(), farBlock)

	bc.processFutureBlocks()

	require.Equal(t, 1, bc.futureBlocks.Len())
	_, ok = bc.futureBlocks.Get(farBlock.Hash())
	require.True(t, ok, "far-ahead block should remain queued")
	require.Equal(t, head.Hash(), bc.CurrentBlock().Hash(), "head must not advance")
}

func TestCoreTProcessFutureBlocks_InsertsReadyBlock(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	head := bc.CurrentBlock()
	headHeader, ok := head.Header().(*block.Header)
	require.True(t, ok)

	// A transaction-less child of head (Root unchanged, same trick as
	// coreT_reorg_test.go's coreTBuildEmptyChildBlock) at exactly head+1:
	// the one case processFutureBlocks will actually try to insert.
	nextBlock := coreTBuildEmptyChildBlock(headHeader, 10)
	bc.futureBlocks.Add(nextBlock.Hash(), nextBlock)

	bc.processFutureBlocks()

	require.Equal(t, 0, bc.futureBlocks.Len(), "ready block should leave the queue")
	require.Equal(t, nextBlock.Hash(), bc.CurrentBlock().Hash(), "head should advance to the inserted block")
}
