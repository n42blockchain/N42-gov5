package internal

// coreT_recover_ancestors_test.go covers recoverAncestors' success path (a
// single ready child is found and inserted) and its ErrUnknownAncestor path
// (the parent chain walk runs off the end without ever finding stored state).

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

func TestCoreTRecoverAncestors_Success(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	head := bc.CurrentBlock()
	headHeader, ok := head.Header().(*block.Header)
	require.True(t, ok)

	child := coreTBuildEmptyChildBlock(headHeader, 10)

	gotHash, err := bc.recoverAncestors(child, false)
	require.NoError(t, err)
	require.Equal(t, child.Hash(), gotHash)
	require.Equal(t, child.Hash(), bc.CurrentBlock().Hash())
}

func TestCoreTRecoverAncestors_UnknownAncestor(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain

	head := bc.CurrentBlock()
	headHeader, ok := head.Header().(*block.Header)
	require.True(t, ok)

	// A child whose parent hash does not resolve to any stored block at
	// number-1: the ancestor walk runs out without ever finding state.
	orphanHeader := &block.Header{
		ParentHash: types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"),
		Number:     uint256.NewInt(0).Add(headHeader.Number, uint256.NewInt(1)),
		GasLimit:   headHeader.GasLimit,
		Time:       headHeader.Time + 10,
		Coinbase:   headHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Root:       headHeader.Root,
	}
	orphan := block.NewBlockFromReceipt(orphanHeader, nil, nil, nil, nil).(*block.Block)

	_, err := bc.recoverAncestors(orphan, false)
	require.ErrorIs(t, err, consensus.ErrUnknownAncestor)
	require.Equal(t, head.Hash(), bc.CurrentBlock().Hash(), "head must not advance")
}
