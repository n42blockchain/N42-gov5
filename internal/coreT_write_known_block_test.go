package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCoreTWriteKnownBlockSameHead calls writeKnownBlock with the current
// head block itself. Since blk.ParentHash() (the head's parent) differs from
// current.Hash() (the head itself), this always takes the reorg branch
// inside writeKnownBlock even though "reorging" onto the current head is a
// no-op in substance -- exercising that path without needing a real
// alternate branch.
func TestCoreTWriteKnownBlockSameHead(t *testing.T) {
	f := coreTGetChainFixture(t)
	bc := f.Chain

	head := bc.CurrentBlock()
	err := bc.writeKnownBlock(nil, head)
	require.NoError(t, err)
	require.Equal(t, head.Hash(), bc.CurrentBlock().Hash())
}
