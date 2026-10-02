package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAggregator_BuildFilesInBackgroundFullCycle drives the real background
// scheduler (BuildFilesInBackground -> buildFilesInBackground per step ->
// MergeLoop -> BuildOptionalMissedIndicesInBackground) end to end, with a
// tiny aggregationStep=1 so the whole cycle finishes in well under a second.
// Unlike lsTSeedAggregator, data here is written but NOT pre-built into step
// files, so BuildFilesInBackground has real work to discover from the DB.
func TestAggregator_BuildFilesInBackgroundFullCycle(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 1)
	agg.KeepInDB(0)

	addr := make([]byte, 20)
	var prev []byte
	const txs = uint64(40)
	for txNum := uint64(1); txNum <= txs; txNum++ {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		lsTWriteAccount(t, agg, tx, txNum, addr, prev)
		require.NoError(t, tx.Commit())
		prev = []byte{byte(txNum)}
	}

	agg.BuildFilesInBackground(txs)

	deadline := time.Now().Add(10 * time.Second)
	for (agg.buildingFiles.Load() || agg.mergeingFiles.Load() || agg.buildingOptionalIndices.Load()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	require.False(t, agg.buildingFiles.Load())
	require.False(t, agg.mergeingFiles.Load())
	require.NotEmpty(t, agg.Files())
}
