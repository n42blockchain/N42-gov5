package membatchwithdb

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/log/v3"
)

var errCovWalker = errors.New("cov walker stop")

// ForEach must propagate a walker error.
func TestCov3ForEachWalkerError(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	err := batch.ForEach(kv.HashedAccounts, nil, func(k, v []byte) error {
		return errCovWalker
	})
	require.ErrorIs(t, err, errCovWalker)
}

// ForPrefix must propagate a walker error.
func TestCov3ForPrefixWalkerError(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	err := batch.ForPrefix(kv.HashedAccounts, []byte("C"), func(k, v []byte) error {
		return errCovWalker
	})
	require.ErrorIs(t, err, errCovWalker)
}

// ForAmount must propagate a walker error.
func TestCov3ForAmountWalkerError(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	err := batch.ForAmount(kv.HashedAccounts, []byte("A"), 5, func(k, v []byte) error {
		return errCovWalker
	})
	require.ErrorIs(t, err, errCovWalker)
}

// A zero limit on Range/RangeDupSort should produce an iterator that never
// yields anything (HasNext's limit==0 short-circuit).
func TestCov3RangeAscendZeroLimit(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	it, err := batch.RangeAscend(kv.HashedAccounts, nil, nil, 0)
	require.NoError(t, err)
	require.False(t, it.HasNext())
}

func TestCov3RangeDupSortZeroLimit(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	it, err := batch.RangeDupSort(kv.AccountChangeSet, []byte("key1"), nil, nil, order.Asc, 0)
	require.NoError(t, err)
	require.False(t, it.HasNext())
}

// Prefix on a prefix with no valid "next subtree" (all 0xFF bytes) falls back
// to Stream(prefix, nil), exercising the "!ok" branch of kv.NextSubtree.
func TestCov3PrefixAllFF(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	rwTx.Put(kv.HashedAccounts, []byte{0xFF, 0xFF}, []byte("tail"))
	rwTx.Put(kv.HashedAccounts, []byte{0xFF, 0xFF, 0x01}, []byte("tail2"))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	it, err := batch.Prefix(kv.HashedAccounts, []byte{0xFF, 0xFF})
	require.NoError(t, err)

	var got []string
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{string([]byte{0xFF, 0xFF}), string([]byte{0xFF, 0xFF, 0x01})}, got)
}

// initSequences must copy multiple pre-existing sequence entries from the
// base tx into the in-memory tx, exercising more than a single loop pass.
func TestCov3InitSequencesMultipleEntries(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	require.NoError(t, rwTx.Put(kv.Sequence, []byte("bucketA"), []byte{0, 0, 0, 0, 0, 0, 0, 5}))
	require.NoError(t, rwTx.Put(kv.Sequence, []byte("bucketB"), []byte{0, 0, 0, 0, 0, 0, 0, 9}))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	seqA, err := batch.ReadSequence("bucketA")
	require.NoError(t, err)
	require.EqualValues(t, 5, seqA)

	seqB, err := batch.ReadSequence("bucketB")
	require.NoError(t, err)
	require.EqualValues(t, 9, seqB)
}

// Diff/Flush must also replay deleted dup entries recorded via DeleteExact on
// a pure-dupsort table.
func TestCov3DiffFlushDeletedDups(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))
	c.Close()

	diff, err := batch.Diff()
	require.NoError(t, err)

	_, targetTx := memdb.NewTestTx(t)
	covInitDupSort(targetTx)
	require.NoError(t, diff.Flush(targetTx))

	var got []string
	err = targetTx.ForEach(kv.AccountChangeSet, nil, func(k, v []byte) error {
		got = append(got, string(k)+":"+string(v))
		return nil
	})
	require.NoError(t, err)
	require.NotContains(t, got, "key1:value1.1")
	require.Contains(t, got, "key1:value1.3")
}

// getNextOnDb's skip-loop runs across more than one consecutive deleted
// underlying entry (Dup variant) before finding a live one.
func TestCov3GetNextOnDbSkipsMultipleDeletedDups(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.1"))
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.2"))
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.3"))
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.4"))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.2")))
	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.3")))

	k, v, err := c.SeekExact([]byte("key1"))
	require.NoError(t, err)
	require.Equal(t, []byte("key1"), k)
	require.Equal(t, []byte("value1.1"), v)

	_, v, err = c.NextDup()
	require.NoError(t, err)
	assert.Equal(t, []byte("value1.4"), v)

	_, v, err = c.NextDup()
	require.NoError(t, err)
	assert.Nil(t, v)
}

// Current(): after a cleared bucket, Current delegates straight to the
// underlying memory cursor's Current().
func TestCov3CurrentAfterClearBucket(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.ClearBucket(kv.HashedAccounts))
	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("ZZZZ"), []byte("zval")))

	c, err := batch.Cursor(kv.HashedAccounts)
	require.NoError(t, err)
	defer c.Close()

	k, v, err := c.First()
	require.NoError(t, err)
	require.Equal(t, []byte("ZZZZ"), k)
	require.Equal(t, []byte("zval"), v)

	k, v, err = c.Current()
	require.NoError(t, err)
	assert.Equal(t, []byte("ZZZZ"), k)
	assert.Equal(t, []byte("zval"), v)
}
