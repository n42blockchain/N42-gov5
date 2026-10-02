package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAggregator_BuildFilesEarlyReturnWhenBelowKeepInDB(t *testing.T) {
	_, _, agg := lsTNewAggregator(t, 4)
	// toTxNum=0 is well below minimax(0)+aggregationStep+keepInDB, so
	// BuildFilesInBackground should no-op and BuildFiles should return
	// immediately without entering its polling loop.
	require.NoError(t, agg.BuildFiles(0))
}

func TestAggregator_OpenListRoundTrip(t *testing.T) {
	agg, _, _, _ := lsTSeedAggregator(t, 4, 16)
	files := agg.Files()
	require.NotEmpty(t, files)
	require.NoError(t, agg.OpenList(files))
}

func TestAggregator_FlushAndBatchWriteLocks(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 4)

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	agg.SetTx(tx)
	agg.StartWrites()

	require.NoError(t, agg.Flush(ctx, tx))
	agg.FinishWrites()

	a2 := agg.BatchHistoryWriteStart()
	require.Same(t, agg, a2)
	agg.BatchHistoryWriteEnd()
}

func TestAggregator_LastIdInDBEmptyTable(t *testing.T) {
	_, db, agg := lsTNewAggregator(t, 4)
	require.EqualValues(t, 0, lastIdInDB(db, agg.accounts.indexKeysTable))
}

func TestBackgroundResult_SetHasGetAndReset(t *testing.T) {
	var br BackgroundResult
	require.False(t, br.Has())

	br.Set(nil)
	require.True(t, br.Has())

	has, err := br.GetAndReset()
	require.True(t, has)
	require.NoError(t, err)

	// Reset clears state.
	require.False(t, br.Has())
}

func TestAggregator_BuildMissedIndicesNoFilesIsNoop(t *testing.T) {
	ctx := context.Background()
	_, _, agg := lsTNewAggregator(t, 4)
	require.NoError(t, agg.BuildMissedIndices(ctx, 1))
}

func TestAggregator_OnFreezeCallback(t *testing.T) {
	_, _, agg := lsTNewAggregator(t, 4)
	called := false
	agg.OnFreeze(func(frozenFileNames []string) { called = true })
	agg.onFreeze(nil)
	require.True(t, called)
}
