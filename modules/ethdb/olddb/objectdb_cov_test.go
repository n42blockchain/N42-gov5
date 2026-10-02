package olddb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
)

func TestObjectDatabasePutGetHasDelete(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	require.NoError(t, db.Put(modules.Account, []byte("k1"), []byte("v1")))

	has, err := db.Has(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.True(t, has)

	v, err := db.GetOne(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	v2, err := db.Get(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v2)

	require.NoError(t, db.Delete(modules.Account, []byte("k1")))
	has, err = db.Has(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.False(t, has)

	_, err = db.Get(modules.Account, []byte("missing"))
	require.Error(t, err)
}

func TestObjectDatabaseAppendAndLast(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	require.NoError(t, db.Append(modules.Account, []byte("a1"), []byte("va1")))
	require.NoError(t, db.Append(modules.Account, []byte("a2"), []byte("va2")))

	k, v, err := db.Last(modules.Account)
	require.NoError(t, err)
	require.Equal(t, []byte("a2"), k)
	require.Equal(t, []byte("va2"), v)
}

func TestObjectDatabaseAppendDup(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	require.NoError(t, db.AppendDup(modules.Storage, []byte("s1"), []byte("sv1")))
}

func TestObjectDatabaseSequence(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	prev, err := db.IncrementSequence(modules.Sequence, 5)
	require.NoError(t, err)
	require.Equal(t, uint64(0), prev)

	cur, err := db.ReadSequence(modules.Sequence)
	require.NoError(t, err)
	require.Equal(t, uint64(5), cur)
}

func TestObjectDatabaseForEachForPrefixForAmount(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	require.NoError(t, db.Put(modules.Account, []byte("p1"), []byte("v1")))
	require.NoError(t, db.Put(modules.Account, []byte("p2"), []byte("v2")))

	var seen []string
	require.NoError(t, db.ForEach(modules.Account, nil, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, db.ForPrefix(modules.Account, []byte("p"), func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, db.ForAmount(modules.Account, nil, 1, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 1)
}

func TestObjectDatabaseBucketLifecycle(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	exists, err := db.BucketExists(modules.Account)
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, db.Put(modules.Account, []byte("x"), []byte("y")))
	require.NoError(t, db.ClearBuckets(modules.Account))

	has, err := db.Has(modules.Account, []byte("x"))
	require.NoError(t, err)
	require.False(t, has)

	require.NoError(t, db.DropBuckets(kv.ChaindataDeprecatedTables[0]))
}

func TestObjectDatabaseRwKV(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)

	require.Equal(t, kvDB, db.RwKV())

	kvDB2 := memdb.NewTestDB(t)
	db.SetRwKV(kvDB2)
	require.Equal(t, kvDB2, db.RwKV())
	db.Close()
}

func TestObjectDatabaseBegin(t *testing.T) {
	kvDB := memdb.NewTestDB(t)
	db := NewObjectDatabase(kvDB)
	defer db.Close()

	batch, err := db.Begin(context.Background(), ethdb.RO)
	require.NoError(t, err)
	require.NotNil(t, batch)
	batch.Rollback()
}
