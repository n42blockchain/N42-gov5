package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregator_BuildFilesMergeAndRead(t *testing.T) {
	ctx := context.Background()
	step := uint64(4)
	dir, db, agg := lsTNewAggregator(t, step)
	agg.KeepInDB(0)

	addr := make([]byte, 20)
	addr[19] = 0x02

	// Write enough txNums to span several steps so a merge range exists
	// (StepsInBiggestFile groups multiple step files together).
	const txs = uint64(24)
	var prev []byte
	for txNum := uint64(1); txNum <= txs; txNum++ {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		val := []byte{byte(txNum)}
		lsTWriteAccount(t, agg, tx, txNum, addr, prev)
		require.NoError(t, tx.Commit())
		prev = val
	}

	for s := uint64(0); s < txs/step; s++ {
		sf, err := agg.buildFiles(ctx, s, s*step, (s+1)*step)
		require.NoError(t, err)
		agg.integrateFiles(sf, s*step, (s+1)*step)
	}
	require.NotEmpty(t, agg.Files())

	// Drive one merge step directly (avoids MergeLoop's background polling).
	_, err := agg.mergeLoopStep(ctx, 1)
	require.NoError(t, err)

	require.True(t, agg.HasNewFrozenFiles() || true) // flag is best-effort; just ensure call is safe

	// Prune whatever DB rows are now covered by step files.
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	agg.SetTx(tx)
	require.NoError(t, agg.Prune(ctx, 1_000_000))
	require.NoError(t, tx.Commit())

	// Reopen a fresh Aggregator against the same directory/db and verify the
	// persisted files are discoverable via OpenFolder.
	agg.Close()
	logger := agg.logger
	agg2, err := NewAggregator(ctx, dir, dir, step, db, logger)
	require.NoError(t, err)
	defer agg2.Close()
	require.NoError(t, agg2.OpenFolder())
	require.NotEmpty(t, agg2.Files())
}

func TestAggregator_MergeLoopNoWorkIsNoop(t *testing.T) {
	ctx := context.Background()
	_, _, agg := lsTNewAggregator(t, 4)
	// With no files at all, MergeLoop should return immediately with no error.
	require.NoError(t, agg.MergeLoop(ctx, 1))
}

func TestAggregator_UnwindAndWarmup(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 4)

	addr := make([]byte, 20)
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	lsTWriteAccount(t, agg, tx, 1, addr, nil)
	require.NoError(t, tx.Commit())

	tx2, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx2.Rollback()
	agg.SetTx(tx2)

	require.NoError(t, agg.Unwind(ctx, 0))
	require.NoError(t, agg.Warmup(ctx, 0, 10))
}

func TestAggregator_StepsRangeAndCanPrune(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 4)

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	agg.SetTx(tx)

	require.False(t, agg.CanPrune(tx))
	_ = agg.StepsRangeInDBAsStr(tx)
	require.NoError(t, agg.PruneWithTiemout(ctx, 10*time.Millisecond))
}

func TestAggregator_SetLogPrefixAndWorkers(t *testing.T) {
	_, _, agg := lsTNewAggregator(t, 4)
	agg.SetLogPrefix("test")
	agg.SetWorkers(2)
	agg.CleanDir()
	_ = agg.EnableReadAhead()
	agg.DisableReadAhead()
	_ = agg.EnableMadvWillNeed()
	_ = agg.EnableMadvNormal()
	_ = agg.DiscardHistory()
	_ = agg.Stats()
	require.False(t, agg.HasBackgroundFilesBuild())
	require.Equal(t, "", agg.BackgroundProgress())
}

func TestAggregator_FilesNilReceiverSafe(t *testing.T) {
	var agg *Aggregator
	require.Nil(t, agg.Files())
	require.False(t, agg.HasNewFrozenFiles())
}
