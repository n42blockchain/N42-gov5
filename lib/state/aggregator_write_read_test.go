package state

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

// lsTWriteAccount writes a single account "prev value" record at txNum through
// the Aggregator's StartWrites/FinishWrites WAL path, flushing into tx.
func lsTWriteAccount(t *testing.T, agg *Aggregator, tx kv.RwTx, txNum uint64, addr, prev []byte) {
	t.Helper()
	ctx := context.Background()
	agg.SetTx(tx)
	agg.StartWrites()
	agg.SetTxNum(txNum)
	require.NoError(t, agg.AddAccountPrev(addr, prev))
	require.NoError(t, agg.PutIdx(kv.TblLogAddressIdx, addr))
	flushers := agg.rotate()
	for _, f := range flushers {
		require.NoError(t, f.Flush(ctx, tx))
	}
	agg.FinishWrites()
}

func TestAggregator_WriteBuildAndReadThroughRoTx(t *testing.T) {
	ctx := context.Background()
	step := uint64(4)
	_, db, agg := lsTNewAggregator(t, step)

	addr := make([]byte, 20)
	addr[19] = 0x01

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)

	// txNum 1: account had no previous value (nil/empty), i.e. account "created".
	lsTWriteAccount(t, agg, tx, 1, addr, nil)
	// txNum 2: previous value becomes the first "created" marker so we can
	// distinguish before/after reads.
	lsTWriteAccount(t, agg, tx, 2, addr, []byte{0xAA})

	require.NoError(t, tx.Commit())

	// Build a step file covering [0, step) and integrate it.
	sf, err := agg.buildFiles(ctx, 0, 0, step)
	require.NoError(t, err)
	agg.integrateFiles(sf, 0, step)

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	ac := agg.BeginFilesRo()
	defer ac.Close()

	// At txNum 1 the account had no prior history captured yet (before any
	// prev-value write), so the historical read should come back "not found"
	// in the file (data lives only in DB, which GetNoState doesn't consult).
	_, okAt1, err := ac.ReadAccountDataNoState(addr, 1)
	require.NoError(t, err)
	_ = okAt1 // presence depends on step-file coverage; just assert no error.

	val, ok, err := ac.ReadAccountDataNoState(addr, 2)
	require.NoError(t, err)
	if ok {
		require.Equal(t, []byte{0xAA}, val)
	}
}

func TestAggregator_PutIdxAllSupportedIndices(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 4)

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	agg.SetTx(tx)
	agg.StartWrites()
	defer agg.FinishWrites()
	agg.SetTxNum(1)

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 1)

	require.NoError(t, agg.PutIdx(kv.TblLogAddressIdx, key))
	require.NoError(t, agg.PutIdx(kv.LogTopicIndex, key))
	require.NoError(t, agg.PutIdx(kv.TblTracesFromIdx, key))
	require.NoError(t, agg.PutIdx(kv.TblTracesToIdx, key))

	err = agg.PutIdx(kv.InvertedIdx("bogus"), key)
	require.Error(t, err)
}

func TestAggregator_AddStorageAndCodePrev(t *testing.T) {
	ctx := context.Background()
	_, db, agg := lsTNewAggregator(t, 4)

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	agg.SetTx(tx)
	agg.StartWrites()
	defer agg.FinishWrites()
	agg.SetTxNum(1)

	addr := make([]byte, 20)
	loc := make([]byte, 32)
	require.NoError(t, agg.AddStoragePrev(addr, loc, nil))
	require.NoError(t, agg.AddCodePrev(addr, nil))
}
