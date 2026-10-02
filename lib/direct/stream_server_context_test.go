package direct

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// These *StreamS types implement the server side of each bidirectional
// stream wrapper; their Send/Err paths are already covered indirectly via
// the client-facing tests in this package, but Context() on the
// server-side type is never invoked by any client path. Call it directly
// here purely to close that last gap.
func TestStreamServerSideContextMethods(t *testing.T) {
	ctx := context.Background()

	ss := &SubscribeStreamS{ctx: ctx}
	require.Equal(t, ctx, ss.Context())

	mpb := &MiningOnPendingBlockS{ctx: ctx}
	require.Equal(t, ctx, mpb.Context())

	ms := &MiningOnMinedBlockS{ctx: ctx}
	require.Equal(t, ctx, ms.Context())

	mpl := &MiningOnPendingLogsS{ctx: ctx}
	require.Equal(t, ctx, mpl.Context())

	sdiff := &StateDiffStreamS{ctx: ctx}
	require.Equal(t, ctx, sdiff.Context())

	tp := &TxPoolOnAddS{ctx: ctx}
	require.Equal(t, ctx, tp.Context())

	sentinelS := &SentinelSubscribeGossipS{ctx: ctx}
	require.Equal(t, ctx, sentinelS.Context())

	sentryMsgS := &SentryMessagesStreamS{ctx: ctx}
	require.Equal(t, ctx, sentryMsgS.Context())

	sentryPeersS := &SentryPeersStreamS{ctx: ctx}
	require.Equal(t, ctx, sentryPeersS.Context())
}
