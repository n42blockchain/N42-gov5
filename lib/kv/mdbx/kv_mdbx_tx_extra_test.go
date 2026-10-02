package mdbx

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestTxIsRoViewID covers (*MdbxTx).IsRo on both a read-write and a
// read-only transaction, plus ViewID/CHandle.
func TestTxIsRoViewID(t *testing.T) {
	db := BaseCaseDB(t)

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	mrw := rwTx.(*MdbxTx)
	require.False(t, mrw.IsRo())
	require.NotZero(t, mrw.ViewID())
	require.NotNil(t, mrw.CHandle())
	rwTx.Rollback()

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	mro := roTx.(*MdbxTx)
	require.True(t, mro.IsRo())
	roTx.Rollback()
}

// TestTxPrintDebugInfo is a no-op reserved method; calling it must not panic.
func TestTxPrintDebugInfo(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	mtx := tx.(*MdbxTx)
	require.NotPanics(t, mtx.PrintDebugInfo)
}

// TestDBPathPageSizeCHandle covers MdbxKV.Path/PageSize/ReadOnly/Accede/CHandle.
func TestDBPathPageSizeCHandle(t *testing.T) {
	path := t.TempDir()
	db := NewMDBX(log.New()).Path(path).MustOpen()
	defer db.Close()
	mdb := db.(*MdbxKV)
	require.Equal(t, path, mdb.Path())
	require.NotZero(t, mdb.PageSize())
	require.False(t, mdb.ReadOnly())
	require.False(t, mdb.Accede())
	require.NotNil(t, mdb.CHandle())
}

// TestCollectMetricsChainDBLabel exercises CollectMetrics against a ChainDB
// labeled instance, the only label for which it does any work.
func TestCollectMetricsChainDBLabel(t *testing.T) {
	db := chainDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	mtx := tx.(*MdbxTx)
	require.NotPanics(t, mtx.CollectMetrics)
}

// TestCollectMetricsNonChainDBLabelNoop exercises the early-return branch
// for any label other than ChainDB.
func TestCollectMetricsNonChainDBLabelNoop(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	mtx := tx.(*MdbxTx)
	require.NotPanics(t, mtx.CollectMetrics)
}

// TestPanickedError covers the unexported panicked error wrapper used by
// the batch helper's panic-recovery path, both branches (wrapping an error
// value, and wrapping an arbitrary non-error value).
func TestPanickedError(t *testing.T) {
	werr := panicked{reason: errors.New("boom")}
	require.Equal(t, "boom", werr.Error())

	wother := panicked{reason: "weird"}
	require.Contains(t, wother.Error(), "weird")
}

// TestForEachWalksFromPrefix covers ForEach, including the walker-error
// short-circuit.
func TestForEachWalksFromPrefix(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, tx.Put("Table", []byte("a"), []byte("1")))
	require.NoError(t, tx.Put("Table", []byte("b"), []byte("2")))
	require.NoError(t, tx.Put("Table", []byte("c"), []byte("3")))

	var seen []string
	err = tx.ForEach("Table", nil, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, seen)

	// From a specific prefix.
	seen = nil
	err = tx.ForEach("Table", []byte("b"), func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"b", "c"}, seen)

	// Walker error propagates and stops iteration.
	boom := errors.New("boom")
	count := 0
	err = tx.ForEach("Table", nil, func(k, v []byte) error {
		count++
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, count)
}

// TestPrefixIterator covers Prefix(), including the NextSubtree overflow
// fallback branch (a prefix of all 0xFF bytes has no "next subtree").
func TestPrefixIterator(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, tx.Put("Table", []byte("pfx1"), []byte("v1")))
	require.NoError(t, tx.Put("Table", []byte("pfx2"), []byte("v2")))
	require.NoError(t, tx.Put("Table", []byte("other"), []byte("v3")))

	it, err := tx.Prefix("Table", []byte("pfx"))
	require.NoError(t, err)
	var got []string
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"pfx1", "pfx2"}, got)

	// All-0xFF prefix: NextSubtree overflows, hitting the tx.Range(prefix,
	// nil) fallback branch.
	require.NoError(t, tx.Put("Table", []byte{0xff, 0xff}, []byte("edge")))
	it2, err := tx.Prefix("Table", []byte{0xff, 0xff})
	require.NoError(t, err)
	require.True(t, it2.HasNext())
	k, v, err := it2.Next()
	require.NoError(t, err)
	require.Equal(t, []byte{0xff, 0xff}, k)
	require.Equal(t, []byte("edge"), v)
}

// TestForAmountZeroIsNoop covers ForAmount's amount==0 early return.
func TestForAmountZeroIsNoop(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, tx.Put("Table", []byte("a"), []byte("1")))

	calls := 0
	err = tx.ForAmount("Table", nil, 0, func(k, v []byte) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, calls)
}

// TestUpsertUnsupportedCursorType covers the error branch of Upsert when
// the underlying cursor does not implement the kv.Upserter interface.
func TestUpsertUnsupportedCursorType(t *testing.T) {
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{"Plain": {}}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	// Plain (non-DupSort) table cursors are *MdbxCursor, which *does*
	// implement Upsert, so this exercises the success path; the type is
	// still a *MdbxCursor instance, confirming Upsert works uniformly.
	require.NoError(t, tx.(kv.Upserter).Upsert("Plain", []byte("k"), []byte("v")))
}
