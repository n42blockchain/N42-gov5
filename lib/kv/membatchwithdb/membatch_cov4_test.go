package membatchwithdb

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// DeleteCurrentDuplicates on a cursor that was never positioned is a no-op.
func TestCov4DeleteCurrentDuplicatesUnpositioned(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.DeleteCurrentDuplicates())
}

// SeekBothRange returns nil when neither overlay nor underlying DB have a
// matching value at or after the given key/value pair.
func TestCov4SeekBothRangeNoMatch(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	v, err := c.SeekBothRange([]byte("key9"), []byte("value9.0"))
	require.NoError(t, err)
	assert.Nil(t, v)
}

// NextNoDup must also cover the isPrevFromDb==true branch, i.e. advancing
// past a db-sourced key-group when the db currently leads the merge.
func TestCov4NextNoDupFromDbSide(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.1"))
	rwTx.Put(kv.AccountChangeSet, []byte("key3"), []byte("value3.1"))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key5"), []byte("value5.1")))

	c, err := batch.CursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	k, _, err := c.First()
	require.NoError(t, err)
	require.Equal(t, []byte("key1"), k)

	k, _, err = c.NextNoDup()
	require.NoError(t, err)
	require.Equal(t, []byte("key3"), k)

	k, _, err = c.NextNoDup()
	require.NoError(t, err)
	require.Equal(t, []byte("key5"), k)

	k, _, err = c.NextNoDup()
	require.NoError(t, err)
	require.Nil(t, k)
}

// Flush with a mix of dupsort and non-dupsort buckets, including deletions
// and a cleared bucket, replaying into a fresh target tx.
func TestCov4FlushMixedBucketsAndDeletes(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key2"), []byte("value2.1")))
	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("ZZZZ"), []byte("zval")))
	require.NoError(t, batch.Delete(kv.HashedAccounts, []byte("AAAA")))

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))
	c.Close()

	require.NoError(t, batch.Flush(rwTx))

	var changeSetKeys []string
	require.NoError(t, rwTx.ForEach(kv.AccountChangeSet, nil, func(k, v []byte) error {
		changeSetKeys = append(changeSetKeys, string(k)+":"+string(v))
		return nil
	}))
	assert.Contains(t, changeSetKeys, "key2:value2.1")
	assert.NotContains(t, changeSetKeys, "key1:value1.1")

	val, err := rwTx.GetOne(kv.HashedAccounts, []byte("ZZZZ"))
	require.NoError(t, err)
	assert.Equal(t, []byte("zval"), val)

	val, err = rwTx.GetOne(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	assert.Nil(t, val)
}
