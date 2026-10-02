package internal

// prefetchPendingCreditsT_test.go covers prefetchPendingCredits's
// small-set-not-worth-it early return (len(addrs) < 256), the overwhelmingly
// common case in practice.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/state"
)

func TestPrefetchPendingCreditsSkipsSmallSet(t *testing.T) {
	fx := coreTNewChainFixture(t)
	sp := NewStateProcessor(fx.Config, fx.Chain, coreTFakerEngine{})

	ibs := state.New(nil)
	n, err := sp.prefetchPendingCredits(ibs, nil, false, 0, nil)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
