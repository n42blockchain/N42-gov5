package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregator_CanPruneFromWithData(t *testing.T) {
	ctx := context.Background()
	agg, db, _, _ := lsTSeedAggregator(t, 4, 16)

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	// With real keys written to TblTracesToKeys/TblStorageHistoryKeys,
	// CanPruneFrom should decode a real (non math.MaxUint64) txNum.
	got := agg.CanPruneFrom(tx)
	require.Less(t, got, uint64(1)<<63)
}

func TestAggregator_OpenFolderMissingDirErrors(t *testing.T) {
	_, _, agg := lsTNewAggregator(t, 4)
	// Point every component at a nonexistent directory so OpenFolder's
	// underlying directory scan fails.
	missing := t.TempDir() + "/does-not-exist"
	agg.accounts.dir = missing
	agg.storage.dir = missing
	agg.code.dir = missing
	agg.logAddrs.dir = missing
	agg.logTopics.dir = missing
	agg.tracesFrom.dir = missing
	agg.tracesTo.dir = missing

	err := agg.OpenFolder()
	require.Error(t, err)
}

func TestAggregator_BuildOptionalMissedIndicesInBackgroundNoFiles(t *testing.T) {
	ctx := context.Background()
	_, _, agg := lsTNewAggregator(t, 4)

	agg.BuildOptionalMissedIndicesInBackground(ctx, 1)

	// Bounded wait for the background goroutine to finish (no files means
	// it should complete almost immediately).
	deadline := time.Now().Add(2 * time.Second)
	for agg.buildingOptionalIndices.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, agg.buildingOptionalIndices.Load())
}
