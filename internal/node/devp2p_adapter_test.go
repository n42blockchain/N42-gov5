package node

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	n42block "github.com/n42blockchain/N42/common/block"
	n42types "github.com/n42blockchain/N42/common/types"
)

func TestBlockHeaderFromBlockNilBlock(t *testing.T) {
	h, err := blockHeaderFromBlock(nil)
	require.Nil(t, h)
	require.Error(t, err)
}

func TestBlockHeaderFromBlockCopiesHeader(t *testing.T) {
	blk := n42block.NewBlock(&n42block.Header{Number: uint256.NewInt(7)}, nil)
	h, err := blockHeaderFromBlock(blk)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.Equal(t, uint256.NewInt(7), h.Number)
}

func TestDevp2pBlockProviderNilGuards(t *testing.T) {
	var nilProvider *devp2pBlockProvider
	_, _, err := nilProvider.CurrentHead()
	require.Error(t, err)
	_, err = nilProvider.GetHeaderByNumber(1)
	require.Error(t, err)
	_, err = nilProvider.GetHeaderByHash(n42types.Hash{})
	require.Error(t, err)
	require.Nil(t, nilProvider.BlockAccessList(n42types.Hash{}))

	p := &devp2pBlockProvider{node: &Node{}} // blockChain nil
	_, _, err = p.CurrentHead()
	require.Error(t, err)
	_, err = p.GetHeaderByNumber(1)
	require.Error(t, err)
	_, err = p.GetHeaderByHash(n42types.Hash{})
	require.Error(t, err)
	require.Nil(t, p.BlockAccessList(n42types.Hash{}))
}

func TestStartEthereumDevP2PNilNodeOrNonEthereumProfile(t *testing.T) {
	var nilNode *Node
	require.NoError(t, nilNode.startEthereumDevP2P())

	n := &Node{}
	require.NoError(t, n.startEthereumDevP2P()) // zero-value profile is not Ethereum EL
}
