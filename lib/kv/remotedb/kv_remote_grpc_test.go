package remotedb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/order"
)

func TestDBOpenAndVersionCompatibility(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()

	require.True(t, db.ReadOnly())
	require.True(t, db.EnsureVersionCompatibility())
	require.NotNil(t, db.AllTables())
	require.Nil(t, db.CHandle())
}

func TestBeginRoAndViewRoundTrip(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()

	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()
	require.NotZero(t, txn.ViewID())
	txn.(*tx).CollectMetrics()

	_, err = db.BeginRw(ctx)
	require.Error(t, err)
	_, err = db.BeginRwNosync(ctx)
	require.Error(t, err)
	_, err = db.BeginTemporalRw(ctx)
	require.Error(t, err)
	_, err = db.BeginTemporalRwNosync(ctx)
	require.Error(t, err)
	require.Error(t, db.Update(ctx, nil))
	require.Error(t, db.UpdateNosync(ctx, nil))

	var sawKey bool
	require.NoError(t, db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(kv.PlainState, []byte{1})
		require.NoError(t, err)
		sawKey = v != nil
		return nil
	}))
	require.True(t, sawKey)
}

func TestBeginRoCancelledContext(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := db.BeginRo(ctx)
	require.Error(t, err)
}

func TestBeginTemporalRo(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()

	ttx, err := db.BeginTemporalRo(ctx)
	require.NoError(t, err)
	defer ttx.Rollback()
	require.NotNil(t, ttx)
}

func TestViewTemporal(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()

	err := db.ViewTemporal(ctx, func(tx kv.TemporalTx) error {
		return nil
	})
	require.NoError(t, err)
}

func TestCursorIterationPlainAndDupSort(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()

	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()

	c, err := txn.Cursor(kv.PlainState)
	require.NoError(t, err)
	defer c.Close()

	k, v, err := c.First()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, k)
	require.Equal(t, []byte{1}, v)

	k, v, err = c.Next()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, k)
	require.Equal(t, []byte{2}, v)

	k, v, err = c.Current()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, k)

	k, v, err = c.Seek([]byte{2})
	require.NoError(t, err)
	require.Equal(t, []byte{2}, k)

	k, v, err = c.Last()
	require.NoError(t, err)
	require.Equal(t, []byte{3}, k)

	k, v, err = c.Prev()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, k)

	k, v, err = c.SeekExact([]byte{9})
	require.NoError(t, err)
	require.Nil(t, k)

	cnt, err := c.Count()
	require.NoError(t, err)
	require.Equal(t, uint64(4), cnt)

	dupTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer dupTx.Rollback()
	dc, err := dupTx.CursorDupSort(kv.PlainState)
	require.NoError(t, err)
	defer dc.Close()

	v2, err := dc.SeekBothRange([]byte{1}, []byte{1})
	require.NoError(t, err)
	require.Equal(t, []byte{1}, v2)

	firstV, err := dc.FirstDup()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, firstV)

	nk, nv, err := dc.NextDup()
	require.NoError(t, err)
	require.Equal(t, []byte{1}, nk)
	require.Equal(t, []byte{2}, nv)

	lastV, err := dc.LastDup()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, lastV)

	nnk, _, err := dc.NextNoDup()
	require.NoError(t, err)
	require.Equal(t, []byte{2}, nnk)

	bk, bv, err := dc.SeekBothExact([]byte{1}, []byte{2})
	require.NoError(t, err)
	require.Equal(t, []byte{1}, bk)
	require.Equal(t, []byte{2}, bv)

	_ = v
}

func TestForEachForPrefixForAmount(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()

	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()

	var seen [][]byte
	require.NoError(t, txn.ForEach(kv.PlainState, nil, func(k, v []byte) error {
		seen = append(seen, append([]byte{}, k...))
		return nil
	}))
	require.NotEmpty(t, seen)

	seen = nil
	require.NoError(t, txn.ForPrefix(kv.PlainState, []byte{1}, func(k, v []byte) error {
		seen = append(seen, append([]byte{}, k...))
		return nil
	}))
	require.NotEmpty(t, seen)

	seen = nil
	require.NoError(t, txn.ForAmount(kv.PlainState, []byte{1}, 2, func(k, v []byte) error {
		seen = append(seen, append([]byte{}, k...))
		return nil
	}))
	require.Len(t, seen, 2)

	require.NoError(t, txn.ForAmount(kv.PlainState, []byte{1}, 0, func(k, v []byte) error {
		t.Fatal("should not be called with amount 0")
		return nil
	}))
}

func TestHasAndGetOne(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()
	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()

	ok, err := txn.Has(kv.PlainState, []byte{1})
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = txn.Has(kv.PlainState, []byte{9})
	require.NoError(t, err)
	require.False(t, ok)

	v, err := txn.GetOne(kv.PlainState, []byte{2})
	require.NoError(t, err)
	require.Equal(t, []byte{1}, v)
}

func TestRangeAndPrefix(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()
	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()

	it, err := txn.Range(kv.PlainState, nil, nil)
	require.NoError(t, err)
	var count int
	for it.HasNext() {
		_, _, err := it.Next()
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 4, count)

	ascIt, err := txn.RangeAscend(kv.PlainState, nil, nil, -1)
	require.NoError(t, err)
	require.True(t, ascIt.HasNext())

	descIt, err := txn.RangeDescend(kv.PlainState, nil, nil, -1)
	require.NoError(t, err)
	require.True(t, descIt.HasNext())

	prefIt, err := txn.Prefix(kv.PlainState, []byte{1})
	require.NoError(t, err)
	require.True(t, prefIt.HasNext())

	_, err = txn.RangeDupSort(kv.PlainState, []byte{1}, nil, nil, order.Asc, -1)
	require.ErrorIs(t, err, errRemoteRangeDupSortUnsupported)
}

func TestTemporalMethodsWithoutServerSupport(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()
	ttx, err := db.BeginTemporalRo(ctx)
	require.NoError(t, err)
	defer ttx.Rollback()

	_, _, err = ttx.DomainGet(kv.Domain("PlainState"), []byte{1}, nil)
	require.Error(t, err)

	_, _, err = ttx.DomainGetAsOf(kv.Domain("PlainState"), []byte{1}, nil, 0)
	require.Error(t, err)

	_, _, err = ttx.HistoryGet(kv.History("PlainState"), []byte{1}, 0)
	require.Error(t, err)

	_, err = ttx.DomainRange(kv.Domain("PlainState"), nil, nil, 0, order.Asc, -1)
	require.NoError(t, err) // error surfaces lazily on first Next()
	it, _ := ttx.DomainRange(kv.Domain("PlainState"), nil, nil, 0, order.Asc, -1)
	require.True(t, it.HasNext())
	_, _, err = it.Next()
	require.Error(t, err)

	hit, _ := ttx.HistoryRange(kv.History("PlainState"), 0, 0, order.Asc, -1)
	require.True(t, hit.HasNext())
	_, _, err = hit.Next()
	require.Error(t, err)

	iit, _ := ttx.IndexRange(kv.InvertedIdx("PlainState"), []byte{1}, 0, 0, order.Asc, -1)
	require.True(t, iit.HasNext())
	_, err = iit.Next()
	require.Error(t, err)
}

func TestRollbackIsIdempotent(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()
	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	txn.Rollback()
	// a second Rollback must be a no-op, not a panic or hang
	require.NotPanics(t, func() { txn.Rollback() })
}

func TestListBucketsNotImplemented(t *testing.T) {
	db := newTestRemoteDB(t)
	defer db.Close()
	ctx := context.Background()
	txn, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer txn.Rollback()

	_, err = txn.ListBuckets()
	require.Error(t, err)
}

func TestMustOpenPanicsOnError(t *testing.T) {
	// Open() in this codebase never actually errors, so MustOpen's happy
	// path is what's reachable; call it to cover that branch.
	opts := NewRemote(gointerfaces.Version{Major: 1}, nil, nil)
	require.NotPanics(t, func() {
		db := opts.MustOpen()
		require.NotNil(t, db)
	})
}
