package olddb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestTxDbPutGetHasDelete(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	require.NoError(t, txDB.Put(modules.Account, []byte("k1"), []byte("v1")))

	has, err := txDB.Has(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.True(t, has)

	v, err := txDB.GetOne(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	v2, err := txDB.Get(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v2)

	require.Greater(t, txDB.BatchSize(), 0)

	require.NoError(t, txDB.Delete(modules.Account, []byte("k1")))
	// NOTE: TxDb.Has delegates to Get, which returns ethdb.ErrKeyNotFound for
	// a missing key, so Has errors (rather than just returning false) once
	// the key is gone. This mirrors existing (pre-existing) behavior; not
	// something introduced or fixed by this test.
	has, err = txDB.Has(modules.Account, []byte("k1"))
	require.Error(t, err)
	require.False(t, has)

	_, err = txDB.Get(modules.Account, []byte("missing"))
	require.Error(t, err)
}

func TestTxDbAppendAndAppendDup(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	require.NoError(t, txDB.Append(modules.Account, []byte("a1"), []byte("va1")))
	require.NoError(t, txDB.AppendDup(modules.AccountChangeSet, []byte("s1"), []byte("sv1")))
}

func TestTxDbLast(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	require.NoError(t, txDB.Put(modules.Account, []byte("a1"), []byte("v1")))
	require.NoError(t, txDB.Put(modules.Account, []byte("a2"), []byte("v2")))

	k, v, err := txDB.Last(modules.Account)
	require.NoError(t, err)
	require.Equal(t, []byte("a2"), k)
	require.Equal(t, []byte("v2"), v)
}

func TestTxDbSequence(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	prev, err := txDB.IncrementSequence(modules.Sequence, 3)
	require.NoError(t, err)
	require.Equal(t, uint64(0), prev)

	cur, err := txDB.ReadSequence(modules.Sequence)
	require.NoError(t, err)
	require.Equal(t, uint64(3), cur)
}

func TestTxDbForEachForPrefixForAmount(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	require.NoError(t, txDB.Put(modules.Account, []byte("p1"), []byte("v1")))
	require.NoError(t, txDB.Put(modules.Account, []byte("p2"), []byte("v2")))

	var seen []string
	require.NoError(t, txDB.ForEach(modules.Account, nil, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, txDB.ForPrefix(modules.Account, []byte("p"), func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, txDB.ForAmount(modules.Account, nil, 1, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 1)
}

func TestTxDbBucketLifecycle(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)

	exists, err := txDB.BucketExists(modules.Account)
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, txDB.Put(modules.Account, []byte("x"), []byte("y")))
	require.NoError(t, txDB.ClearBuckets(modules.Account))

	has, err := txDB.Has(modules.Account, []byte("x"))
	require.Error(t, err)
	require.False(t, has)

	require.NoError(t, txDB.DropBuckets(kv.ChaindataDeprecatedTables[0]))
}

func TestTxDbCommit(t *testing.T) {
	db := memdb.NewTestDB(t)
	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)

	txDB := WrapIntoTxDB(rwTx)
	require.NoError(t, txDB.Put(modules.Account, []byte("k1"), []byte("v1")))
	require.NoError(t, txDB.Commit())
	require.Nil(t, txDB.Tx())

	err = txDB.Commit()
	require.Error(t, err)
}

func TestTxDbRollbackNoop(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	txDB := WrapIntoTxDB(rwTx)
	txDB.Rollback()
	require.Nil(t, txDB.Tx())
	// second rollback is a no-op
	txDB.Rollback()
}
