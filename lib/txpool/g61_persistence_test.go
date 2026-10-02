package txpool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestG61LastSeenBlockRoundTrip covers LastSeenBlock/PutLastSeenBlock: an
// absent key reads back as 0, a put is visible to a later read within the
// same tx, and a nil scratch buffer is grown by PutLastSeenBlock itself.
func TestG61LastSeenBlockRoundTrip(t *testing.T) {
	db := memdb.NewTestPoolDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	n, err := LastSeenBlock(tx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), n, "absent key should read back as 0")

	require.NoError(t, PutLastSeenBlock(tx, 42, nil))
	n, err = LastSeenBlock(tx)
	require.NoError(t, err)
	require.Equal(t, uint64(42), n)

	// Reuse a shorter buffer: common.EnsureEnoughSize must grow it.
	var buf []byte
	require.NoError(t, PutLastSeenBlock(tx, 1<<40, buf))
	n, err = LastSeenBlock(tx)
	require.NoError(t, err)
	require.Equal(t, uint64(1<<40), n)
}
