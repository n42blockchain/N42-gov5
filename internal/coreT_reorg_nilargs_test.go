package internal

// coreT_reorg_nilargs_test.go covers reorg()'s nil-argument guards and its
// identical-head no-op short circuit.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreTReorg_NilArgs(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	head := bc.CurrentBlock()

	err := bc.reorg(nil, nil, head)
	require.EqualError(t, err, "invalid old chain")

	err = bc.reorg(nil, head, nil)
	require.EqualError(t, err, "invalid new chain")
}

func TestCoreTReorg_SameHeadNoOp(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	head := bc.CurrentBlock()

	err := bc.reorg(nil, head, head)
	require.NoError(t, err)
	require.Equal(t, head.Hash(), bc.CurrentBlock().Hash())
}
