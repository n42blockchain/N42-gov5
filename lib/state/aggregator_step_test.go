package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAggregatorStep_ReadsAndIterators(t *testing.T) {
	// A file is only "frozen" (and thus eligible for MakeSteps, which only
	// considers frozen files) once it spans exactly StepsInBiggestFile (32)
	// steps. Use aggregationStep=1 so 32 single-tx steps merge up into one
	// frozen file without a large fixture.
	agg, _, addr, loc := lsTSeedAggregator(t, 1, StepsInBiggestFile)
	ctx := context.Background()
	for {
		merged, err := agg.mergeLoopStep(ctx, 1)
		require.NoError(t, err)
		if !merged {
			break
		}
	}

	steps, err := agg.MakeSteps()
	require.NoError(t, err)
	require.NotEmpty(t, steps)

	as := steps[0]
	from, to := as.TxNumRange()
	require.LessOrEqual(t, from, to)

	_, _, _ = as.ReadAccountDataNoState(addr, to)
	_, _, _ = as.ReadAccountStorageNoState(addr, loc, to)
	_, _, _ = as.ReadAccountCodeNoState(addr, to)
	_, _, _ = as.ReadAccountCodeSizeNoState(addr, to)

	_, _ = as.MaxTxNumAccounts(addr)
	_, _ = as.MaxTxNumStorage(addr, loc)
	_, _ = as.MaxTxNumCode(addr)

	accIt := as.IterateAccountsTxs()
	for accIt.HasNext() {
		_, err := accIt.Next()
		require.NoError(t, err)
	}
	stoIt := as.IterateStorageTxs()
	for stoIt.HasNext() {
		_, err := stoIt.Next()
		require.NoError(t, err)
	}
	codeIt := as.IterateCodeTxs()
	for codeIt.HasNext() {
		_, err := codeIt.Next()
		require.NoError(t, err)
	}

	accHist := as.IterateAccountsHistory(to)
	require.NotNil(t, accHist)
	stoHist := as.IterateStorageHistory(to)
	require.NotNil(t, stoHist)
	codeHist := as.IterateCodeHistory(to)
	require.NotNil(t, codeHist)

	clone := as.Clone()
	require.NotNil(t, clone)
	cFrom, cTo := clone.TxNumRange()
	require.Equal(t, from, cFrom)
	require.Equal(t, to, cTo)
}

func TestAggregator_EndTxNumMinimaxAndLogStats(t *testing.T) {
	agg, db, _, _ := lsTSeedAggregator(t, 4, 16)
	require.GreaterOrEqual(t, agg.EndTxNumMinimax(), uint64(0))
	require.GreaterOrEqual(t, agg.EndTxNumFrozenAndIndexed(), uint64(0))

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	agg.LogStats(tx, func(endTxNumMinimax uint64) uint64 { return endTxNumMinimax })
}
