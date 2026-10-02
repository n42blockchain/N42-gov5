package olddb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
)

func TestMapMutationPutGetHasDeleteAndCommit(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, m.Put(modules.Account, []byte("k1"), []byte("v1")))
	require.NoError(t, m.Put(modules.Account, []byte("k2"), []byte("v2")))

	has, err := m.Has(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.True(t, has)

	v, err := m.GetOne(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	v2, err := m.Get(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v2)

	require.Greater(t, m.BatchSize(), 0)

	require.NoError(t, m.Delete(modules.Account, []byte("k2")))

	require.NoError(t, m.Commit())

	vAfter, err := tx.GetOne(modules.Account, []byte("k1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), vAfter)
}

func TestMapMutationGetMissingKey(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	_, err := m.Get(modules.Account, []byte("missing"))
	require.Error(t, err)
}

func TestMapMutationAppendAndAppendDup(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, m.Append(modules.Account, []byte("a1"), []byte("va1")))
	require.NoError(t, m.AppendDup(modules.Storage, []byte("s1"), []byte("sv1")))
	require.NoError(t, m.Commit())
}

func TestMapMutationSequence(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	prev, err := m.IncrementSequence(modules.Sequence, 3)
	require.NoError(t, err)
	require.Equal(t, uint64(0), prev)

	cur, err := m.ReadSequence(modules.Sequence)
	require.NoError(t, err)
	require.Equal(t, uint64(3), cur)
}

func TestMapMutationForEachForPrefixForAmount(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, tx.Put(modules.Account, []byte("p1"), []byte("v1")))
	require.NoError(t, tx.Put(modules.Account, []byte("p2"), []byte("v2")))

	var seen []string
	require.NoError(t, m.ForEach(modules.Account, nil, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, m.ForPrefix(modules.Account, []byte("p"), func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 2)

	seen = nil
	require.NoError(t, m.ForAmount(modules.Account, nil, 1, func(k, v []byte) error {
		seen = append(seen, string(k))
		return nil
	}))
	require.Len(t, seen, 1)
}

func TestMapMutationLast(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, tx.Put(modules.Account, []byte("a1"), []byte("v1")))
	require.NoError(t, tx.Put(modules.Account, []byte("a2"), []byte("v2")))

	k, v, err := m.Last(modules.Account)
	require.NoError(t, err)
	require.Equal(t, []byte("a2"), k)
	require.Equal(t, []byte("v2"), v)
}

func TestMapMutationRollback(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, m.Put(modules.Account, []byte("k1"), []byte("v1")))
	m.Rollback()
	require.Equal(t, 0, m.BatchSize())
}

func TestMapMutationClose(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	require.NoError(t, m.Put(modules.Account, []byte("k2"), []byte("v2")))
	m.Close()
	require.Equal(t, 0, m.BatchSize())
}

func TestMapMutationCommitNilDB(t *testing.T) {
	m := NewHashBatch(nil, nil, t.TempDir())
	require.NoError(t, m.Commit())
}

func TestMapMutationGetNoDB(t *testing.T) {
	m := NewHashBatch(nil, nil, t.TempDir())
	v, err := m.GetOne(modules.Account, []byte("x"))
	require.NoError(t, err)
	require.Nil(t, v)

	has, err := m.Has(modules.Account, []byte("x"))
	require.NoError(t, err)
	require.False(t, has)
}

func TestMapMutationRwKVAndSetRwKV(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())
	require.Nil(t, m.RwKV())

	m.SetRwKV(nil)
}

func TestMapMutationBeginUnsupported(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	m := NewHashBatch(tx, nil, t.TempDir())

	_, err := m.Begin(context.Background(), ethdb.RW)
	require.ErrorIs(t, err, errMutationBeginUnsupported)
}
