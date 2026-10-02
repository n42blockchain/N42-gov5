package membatchwithdb

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// covInitNonDupSort populates kv.HashedAccounts with a small sorted fixture.
func covInitNonDupSort(rwTx kv.RwTx) {
	rwTx.Put(kv.HashedAccounts, []byte("AAAA"), []byte("value"))
	rwTx.Put(kv.HashedAccounts, []byte("CAAA"), []byte("value1"))
	rwTx.Put(kv.HashedAccounts, []byte("CBAA"), []byte("value2"))
	rwTx.Put(kv.HashedAccounts, []byte("CCAA"), []byte("value3"))
}

// Read-through: a key absent from the overlay falls through to the underlying DB.
func TestCovGetOneReadThrough(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	val, err := batch.GetOne(kv.HashedAccounts, []byte("CAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("value1"), val)
}

// Overwrite: writing a key twice through the overlay returns the latest value,
// and that value - not the underlying one - survives a flush.
func TestCovOverwriteThenFlush(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("AAAA"), []byte("first")))
	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("AAAA"), []byte("second")))

	val, err := batch.GetOne(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("second"), val)

	require.NoError(t, batch.Flush(rwTx))

	val, err = rwTx.GetOne(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("second"), val)
}

// Delete tombstone: deleting a key present only in the underlying DB must not
// be resurrected by a later read-through.
func TestCovDeleteTombstoneSurvivesFlush(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Delete(kv.HashedAccounts, []byte("CBAA")))

	val, err := batch.GetOne(kv.HashedAccounts, []byte("CBAA"))
	require.NoError(t, err)
	require.Nil(t, val)

	require.NoError(t, batch.Flush(rwTx))

	val, err = rwTx.GetOne(kv.HashedAccounts, []byte("CBAA"))
	require.NoError(t, err)
	require.Nil(t, val)
}

// Cursor merge ordering: iterating must interleave overlay and underlying keys
// in sorted order, with deleted underlying keys skipped entirely.
func TestCovCursorMergeOrderingWithDeletes(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("BAAA"), []byte("valueB")))
	require.NoError(t, batch.Delete(kv.HashedAccounts, []byte("CAAA")))

	c, err := batch.Cursor(kv.HashedAccounts)
	require.NoError(t, err)
	defer c.Close()

	var got []string
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"AAAA", "BAAA", "CBAA", "CCAA"}, got)
}

func TestCovNewMemoryBatchWithCustomDBAndUpdateTxn(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	memDb, memTx := memdb.NewTestTx(t)
	batch := NewMemoryBatchWithCustomDB(rwTx, memDb, memTx, "")
	defer batch.Rollback()

	val, err := batch.GetOne(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), val)

	_, rwTx2 := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx2)
	rwTx2.Put(kv.HashedAccounts, []byte("AAAA"), []byte("updated"))
	batch.UpdateTxn(rwTx2)

	val, err = batch.GetOne(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("updated"), val)
}

func TestCovCommitAndMiscHelpers(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("ZZZZ"), []byte("zval")))
	require.NoError(t, batch.Commit())

	size, err := batch.BucketSize(kv.HashedAccounts)
	require.NoError(t, err)
	require.NotZero(t, size)

	require.NoError(t, batch.CreateBucket("some-new-bucket"))
	exists, err := batch.ExistsBucket("some-new-bucket")
	require.NoError(t, err)
	require.True(t, exists)

	batch.CollectMetrics()

	require.NotNil(t, batch.MemDB())
	require.NotNil(t, batch.MemTx())
}

// RangeDescend should walk overlay+underlying keys from highest to lowest.
func TestCovRangeDescend(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("BAAA"), []byte("valueB")))

	it, err := batch.RangeDescend(kv.HashedAccounts, nil, nil, -1)
	require.NoError(t, err)

	var got []string
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"CCAA", "CBAA", "CAAA", "BAAA", "AAAA"}, got)
}

func TestCovPrefixStreamVariants(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	it, err := batch.Prefix(kv.HashedAccounts, []byte("C"))
	require.NoError(t, err)
	var got []string
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"CAAA", "CBAA", "CCAA"}, got)

	it, err = batch.Stream(kv.HashedAccounts, []byte("AAAA"), []byte("CZZZ"))
	require.NoError(t, err)
	got = nil
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"AAAA", "CAAA", "CBAA", "CCAA"}, got)

	it, err = batch.StreamAscend(kv.HashedAccounts, []byte("AAAA"), []byte("DZZZ"), 2)
	require.NoError(t, err)
	got = nil
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"AAAA", "CAAA"}, got)

	it, err = batch.StreamDescend(kv.HashedAccounts, nil, nil, 2)
	require.NoError(t, err)
	got = nil
	for it.HasNext() {
		k, _, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(k))
	}
	require.Equal(t, []string{"CCAA", "CBAA"}, got)
}

func covInitDupSort(rwTx kv.RwTx) {
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.1"))
	rwTx.Put(kv.AccountChangeSet, []byte("key3"), []byte("value3.1"))
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.3"))
}

func TestCovRangeDupSort(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.2")))

	it, err := batch.RangeDupSort(kv.AccountChangeSet, []byte("key1"), nil, nil, order.Asc, -1)
	require.NoError(t, err)

	var got []string
	for it.HasNext() {
		_, v, err := it.Next()
		require.NoError(t, err)
		got = append(got, string(v))
	}
	require.Equal(t, []string{"value1.1", "value1.2", "value1.3"}, got)
}

// Diff() should capture overlay puts/deletes/cleared buckets and Flush them
// onto a target tx, producing the same end state as MemoryMutation.Flush.
func TestCovDiffFlush(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("BAAA"), []byte("valueB")))
	require.NoError(t, batch.Delete(kv.HashedAccounts, []byte("CBAA")))

	diff, err := batch.Diff()
	require.NoError(t, err)

	_, targetTx := memdb.NewTestTx(t)
	covInitNonDupSort(targetTx)
	require.NoError(t, diff.Flush(targetTx))

	val, err := targetTx.GetOne(kv.HashedAccounts, []byte("BAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("valueB"), val)

	val, err = targetTx.GetOne(kv.HashedAccounts, []byte("CBAA"))
	require.NoError(t, err)
	require.Nil(t, val)
}

func TestCovDiffFlushDupSortAndClearedBucket(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key2"), []byte("value2.1")))
	require.NoError(t, batch.ClearBucket(kv.HashedAccounts))

	diff, err := batch.Diff()
	require.NoError(t, err)

	_, targetTx := memdb.NewTestTx(t)
	covInitDupSort(targetTx)
	covInitNonDupSort(targetTx)
	require.NoError(t, diff.Flush(targetTx))

	var keys []string
	err = targetTx.ForEach(kv.AccountChangeSet, nil, func(k, v []byte) error {
		keys = append(keys, string(k))
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, keys, "key2")

	var hashedKeys []string
	err = targetTx.ForEach(kv.HashedAccounts, nil, func(k, v []byte) error {
		hashedKeys = append(hashedKeys, string(k))
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, hashedKeys)
}

// Cursor-level unsupported reverse-iteration / dup helpers that aren't yet
// exercised anywhere else, plus Cursor.Append.
func TestCovCursorAppendAndUnsupported(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursor(kv.HashedAccounts)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Append([]byte("ZZZZ"), []byte("zval")))

	val, err := batch.GetOne(kv.HashedAccounts, []byte("ZZZZ"))
	require.NoError(t, err)
	require.Equal(t, []byte("zval"), val)

	dupCursor, err := batch.RwCursorDupSort(kv.HashedAccounts)
	require.NoError(t, err)
	defer dupCursor.Close()

	_, _, err = dupCursor.PrevDup()
	require.ErrorIs(t, err, errReverseIterationUnsupported)

	_, _, err = dupCursor.PrevNoDup()
	require.ErrorIs(t, err, errReverseIterationUnsupported)

	_, err = dupCursor.LastDup()
	require.ErrorIs(t, err, errDupCountUnsupported)

	_, err = dupCursor.CountDuplicates()
	require.ErrorIs(t, err, errDupCountUnsupported)
}

// SeekBothExact: exercise both the overlay-hit and db-fallback branches.
func TestCovSeekBothExact(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key2"), []byte("value2.1")))

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	// overlay hit
	k, v, err := c.SeekBothExact([]byte("key2"), []byte("value2.1"))
	require.NoError(t, err)
	assert.Equal(t, []byte("key2"), k)
	assert.Equal(t, []byte("value2.1"), v)

	// db fallback
	k, v, err = c.SeekBothExact([]byte("key1"), []byte("value1.1"))
	require.NoError(t, err)
	assert.Equal(t, []byte("key1"), k)
	assert.Equal(t, []byte("value1.1"), v)

	// neither
	k, v, err = c.SeekBothExact([]byte("key1"), []byte("nope"))
	require.NoError(t, err)
	assert.Nil(t, k)
	assert.Nil(t, v)
}
