package internal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestE1ResultServesEarlyResult: with N42_LAG_RESULT_EARLY on, the executor's
// e1Result answers from the published early result (before any hand-off job
// or stored row exists); with it off, the early result is not served.
func TestE1ResultServesEarlyResult(t *testing.T) {
	hash := types.Hash{0x42}
	root := types.Hash{0x07}
	done := make(chan struct{})
	close(done)
	fut := &receiptsResultFuture{hash: hash, done: done}
	want := fut.result
	want.Root = root

	bc := &BlockChain{}
	bc.lagWrite.early.Store(&lagEarlyResult{hash: hash, root: root, future: fut, gone: make(chan struct{})})

	t.Run("on", func(t *testing.T) {
		defer SetLagResultEarlyForTest(true)()
		s0 := lagResultsEarlyServed.Load()
		r, ok, err := bc.e1Result(hash)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, r)
		require.Equal(t, s0+1, lagResultsEarlyServed.Load())
		// Another block's hash is not answered by it.
		_, ok = bc.earlyExecutedResult(types.Hash{0x43})
		require.False(t, ok)
	})
	t.Run("off", func(t *testing.T) {
		defer SetLagResultEarlyForTest(false)()
		s0 := lagResultsEarlyServed.Load()
		_, ok := bc.earlyExecutedResult(hash)
		require.False(t, ok)
		require.Equal(t, s0, lagResultsEarlyServed.Load())
	})
}
