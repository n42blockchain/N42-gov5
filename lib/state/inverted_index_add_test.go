package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvertedIndex_AddUnexportedWrapper(t *testing.T) {
	_, db, agg := lsTNewAggregator(t, 4)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	agg.SetTx(tx)
	agg.logAddrs.StartUnbufferedWrites()
	agg.logAddrs.SetTxNum(1)
	require.NoError(t, agg.logAddrs.add([]byte("key"), []byte("idxkey")))
	agg.logAddrs.FinishWrites()
}

func TestHistory_StartUnbufferedWrites(t *testing.T) {
	_, db, agg := lsTNewAggregator(t, 4)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	agg.SetTx(tx)
	agg.accounts.StartUnbufferedWrites()
	agg.accounts.SetTxNum(1)
	require.NoError(t, agg.AddAccountPrev(make([]byte, 20), nil))
	agg.accounts.FinishWrites()
}
