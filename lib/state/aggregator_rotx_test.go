package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/order"
)

// lsTSeedAggregator writes `txs` sequential account/storage/code/log-index
// records for one key set, builds step files for every full step, and
// returns an open read tx plus a RoTx snapshot for queries.
func lsTSeedAggregator(t *testing.T, step, txs uint64) (*Aggregator, kv.RwDB, []byte, []byte) {
	t.Helper()
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, step)

	addr := make([]byte, 20)
	addr[19] = 0x03
	loc := make([]byte, 32)
	loc[31] = 0x01

	var prevAcc, prevCode []byte
	for txNum := uint64(1); txNum <= txs; txNum++ {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		agg.SetTx(tx)
		agg.StartWrites()
		agg.SetTxNum(txNum)
		require.NoError(t, agg.AddAccountPrev(addr, prevAcc))
		require.NoError(t, agg.AddStoragePrev(addr, loc, nil))
		require.NoError(t, agg.AddCodePrev(addr, prevCode))
		require.NoError(t, agg.PutIdx(kv.TblLogAddressIdx, addr))
		require.NoError(t, agg.PutIdx(kv.LogTopicIndex, addr))
		require.NoError(t, agg.PutIdx(kv.TblTracesFromIdx, addr))
		require.NoError(t, agg.PutIdx(kv.TblTracesToIdx, addr))
		for _, f := range agg.rotate() {
			require.NoError(t, f.Flush(ctx, tx))
		}
		agg.FinishWrites()
		require.NoError(t, tx.Commit())
		prevAcc = []byte{byte(txNum)}
		prevCode = []byte{byte(txNum), 0xCC}
	}

	for s := uint64(0); s < txs/step; s++ {
		sf, err := agg.buildFiles(ctx, s, s*step, (s+1)*step)
		require.NoError(t, err)
		agg.integrateFiles(sf, s*step, (s+1)*step)
	}

	return agg, db, addr, loc
}

func TestAggregatorRoTx_ReadAccountStorageCodePaths(t *testing.T) {
	ctx := context.Background()
	agg, db, addr, loc := lsTSeedAggregator(t, 4, 16)

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	ac := agg.BeginFilesRo()
	defer ac.Close()

	_, _, err = ac.ReadAccountDataNoStateWithRecent(addr, 8, roTx)
	require.NoError(t, err)

	_, _, err = ac.ReadAccountStorageNoState(addr, loc, 8)
	require.NoError(t, err)

	_, _, err = ac.ReadAccountStorageNoStateWithRecent(addr, loc, 8, roTx)
	require.NoError(t, err)

	key := append(append([]byte{}, addr...), loc...)
	_, _, err = ac.ReadAccountStorageNoStateWithRecent2(key, 8, roTx)
	require.NoError(t, err)

	_, _, err = ac.ReadAccountCodeNoState(addr, 8)
	require.NoError(t, err)
	_, _, err = ac.ReadAccountCodeNoStateWithRecent(addr, 8, roTx)
	require.NoError(t, err)

	size, _, err := ac.ReadAccountCodeSizeNoState(addr, 8)
	require.NoError(t, err)
	require.GreaterOrEqual(t, size, 0)

	size2, _, err := ac.ReadAccountCodeSizeNoStateWithRecent(addr, 8, roTx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, size2, 0)
}

func TestAggregatorRoTx_HistoryRangesAndAsOf(t *testing.T) {
	ctx := context.Background()
	agg, db, addr, _ := lsTSeedAggregator(t, 4, 16)

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	ac := agg.BeginFilesRo()
	defer ac.Close()

	_, err = ac.AccountHistoryRange(0, 16, order.Asc, -1, roTx)
	require.NoError(t, err)
	_, err = ac.StorageHistoryRange(0, 16, order.Asc, -1, roTx)
	require.NoError(t, err)
	_, err = ac.CodeHistoryRange(0, 16, order.Asc, -1, roTx)
	require.NoError(t, err)

	_ = ac.AccountHistoricalStateRange(0, nil, nil, -1, roTx)
	_ = ac.StorageHistoricalStateRange(0, nil, nil, -1, roTx)
	_ = ac.CodeHistoricalStateRange(0, nil, nil, -1, roTx)

	_ = addr
}

func TestAggregatorRoTx_IndexRangeAllNamesAndError(t *testing.T) {
	ctx := context.Background()
	agg, db, addr, _ := lsTSeedAggregator(t, 4, 16)

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	ac := agg.BeginFilesRo()
	defer ac.Close()

	names := []kv.InvertedIdx{
		kv.AccountsHistoryIdx, kv.StorageHistoryIdx, kv.CodeHistoryIdx,
		kv.LogTopicIdx, kv.LogAddrIdx, kv.TracesFromIdx, kv.TracesToIdx,
	}
	for _, name := range names {
		_, err := ac.IndexRange(name, addr, 0, 16, order.Asc, -1, roTx)
		require.NoErrorf(t, err, "name=%s", name)
	}

	_, err = ac.IndexRange(kv.InvertedIdx("bogus"), addr, 0, 16, order.Asc, -1, roTx)
	require.Error(t, err)
}

func TestAggregator_MakeSteps(t *testing.T) {
	agg, _, _, _ := lsTSeedAggregator(t, 4, 16)
	steps, err := agg.MakeSteps()
	require.NoError(t, err)
	for _, s := range steps {
		from, to := s.TxNumRange()
		require.LessOrEqual(t, from, to)
	}
}
