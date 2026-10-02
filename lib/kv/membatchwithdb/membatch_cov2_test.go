package membatchwithdb

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// Seek must skip a deleted underlying-DB entry and fall through to the next
// live one, merging correctly with an overlay entry.
func TestCov2SeekSkipsDeletedDbEntry(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Delete(kv.HashedAccounts, []byte("CAAA")))
	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("CABA"), []byte("overlayVal")))

	c, err := batch.Cursor(kv.HashedAccounts)
	require.NoError(t, err)
	defer c.Close()

	k, v, err := c.Seek([]byte("CAAA"))
	require.NoError(t, err)
	// CAAA is deleted, CABA (overlay) sorts before CBAA (db), so it wins.
	assert.Equal(t, []byte("CABA"), k)
	assert.Equal(t, []byte("overlayVal"), v)
}

// Last on a pure-dupsort table exercises the keyCompare branches (equal,
// greater, less) between the overlay's and the underlying DB's last entries.
func TestCov2LastDupSortKeyCompareBranches(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	t.Run("mem last key greater", func(t *testing.T) {
		_, rwTx := memdb.NewTestTx(t)
		covInitDupSort(rwTx)
		batch := NewMemoryBatch(rwTx, "", log.Root())
		defer batch.Close()
		require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key9"), []byte("value9.1")))

		c, err := batch.Cursor(kv.AccountChangeSet)
		require.NoError(t, err)
		defer c.Close()
		k, v, err := c.Last()
		require.NoError(t, err)
		assert.Equal(t, []byte("key9"), k)
		assert.Equal(t, []byte("value9.1"), v)
	})

	t.Run("db last key greater", func(t *testing.T) {
		_, rwTx := memdb.NewTestTx(t)
		covInitDupSort(rwTx)
		batch := NewMemoryBatch(rwTx, "", log.Root())
		defer batch.Close()
		require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key0"), []byte("value0.1")))

		c, err := batch.Cursor(kv.AccountChangeSet)
		require.NoError(t, err)
		defer c.Close()
		k, v, err := c.Last()
		require.NoError(t, err)
		assert.Equal(t, []byte("key3"), k)
		assert.Equal(t, []byte("value3.1"), v)
	})

	t.Run("same key, mem value greater", func(t *testing.T) {
		_, rwTx := memdb.NewTestTx(t)
		covInitDupSort(rwTx)
		batch := NewMemoryBatch(rwTx, "", log.Root())
		defer batch.Close()
		require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key3"), []byte("value3.9")))

		c, err := batch.Cursor(kv.AccountChangeSet)
		require.NoError(t, err)
		defer c.Close()
		k, v, err := c.Last()
		require.NoError(t, err)
		assert.Equal(t, []byte("key3"), k)
		assert.Equal(t, []byte("value3.9"), v)
	})

	t.Run("same key, db value greater", func(t *testing.T) {
		_, rwTx := memdb.NewTestTx(t)
		covInitDupSort(rwTx)
		batch := NewMemoryBatch(rwTx, "", log.Root())
		defer batch.Close()
		require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key3"), []byte("value3.0")))

		c, err := batch.Cursor(kv.AccountChangeSet)
		require.NoError(t, err)
		defer c.Close()
		k, v, err := c.Last()
		require.NoError(t, err)
		assert.Equal(t, []byte("key3"), k)
		assert.Equal(t, []byte("value3.1"), v)
	})

	// NOTE: Last() checks deletion with NextType "Normal" even for a pure
	// dupsort table (memory_mutation_cursor.go Last(), line ~452), where
	// every other path (First/Seek/getNextOnDb) correctly passes "Dup" so
	// isEntryDeleted routes to isDupDeleted. Because of that mismatch, a
	// dup-deleted last entry on a pure-dupsort table is NOT recognized as
	// deleted here, and the stale underlying value is returned instead of
	// falling back to the overlay. This test documents the current
	// (buggy) behavior rather than the ideal one; see final report.
	t.Run("db last dup-deleted entry is NOT recognized by Last (documented defect)", func(t *testing.T) {
		_, rwTx := memdb.NewTestTx(t)
		covInitDupSort(rwTx)
		batch := NewMemoryBatch(rwTx, "", log.Root())
		defer batch.Close()
		require.NoError(t, batch.Put(kv.AccountChangeSet, []byte("key2"), []byte("value2.1")))

		c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
		require.NoError(t, err)
		defer c.Close()
		require.NoError(t, c.DeleteExact([]byte("key3"), []byte("value3.1")))

		k, v, err := c.Last()
		require.NoError(t, err)
		// Ideally this would be key2/value2.1 (the deletion recognized and
		// mem's last entry returned); instead the stale db entry wins.
		assert.Equal(t, []byte("key3"), k)
		assert.Equal(t, []byte("value3.1"), v)
	})
}

// DeleteCurrent / DeleteExact on a non-pure-dupsort table route through the
// normal entry-deletion path rather than the dup-tracking map.
func TestCov2DeleteCurrentAndDeleteExactNonPureDupsort(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.HashedAccounts)
	require.NoError(t, err)
	defer c.Close()

	k, _, err := c.SeekExact([]byte("CAAA"))
	require.NoError(t, err)
	require.Equal(t, []byte("CAAA"), k)
	require.NoError(t, c.DeleteCurrent())

	val, err := batch.GetOne(kv.HashedAccounts, []byte("CAAA"))
	require.NoError(t, err)
	require.Nil(t, val)

	require.NoError(t, c.DeleteExact([]byte("CBAA"), []byte("value2")))
	val, err = batch.GetOne(kv.HashedAccounts, []byte("CBAA"))
	require.NoError(t, err)
	require.Nil(t, val)
}

// SeekBothRange must skip a dup entry that was explicitly deleted.
func TestCov2SeekBothRangeSkipsDeletedDup(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.1"))
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.2"))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))

	v, err := c.SeekBothRange([]byte("key1"), []byte("value1.0"))
	require.NoError(t, err)
	assert.Equal(t, []byte("value1.2"), v)
}

// ForAmount with amount == 0 should be a pure no-op.
func TestCov2ForAmountZero(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	var called bool
	err := batch.ForAmount(kv.HashedAccounts, []byte("A"), 0, func(k, v []byte) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	require.False(t, called)
}

// Has: both a present overlay-only key and a present underlying-DB key.
func TestCov2HasOverlayAndDb(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitNonDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.Put(kv.HashedAccounts, []byte("ZZZZ"), []byte("v")))

	ok, err := batch.Has(kv.HashedAccounts, []byte("ZZZZ"))
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = batch.Has(kv.HashedAccounts, []byte("AAAA"))
	require.NoError(t, err)
	require.True(t, ok)
}

// isDupDeleted direct behaviour: unknown table / unknown key / unknown value.
func TestCov2IsDupDeletedBranches(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	covInitDupSort(rwTx)

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.False(t, batch.isDupDeleted("unknown-table", []byte("k"), []byte("v")))

	c, err := batch.RwCursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))

	require.False(t, batch.isDupDeleted(kv.AccountChangeSet, []byte("key-missing"), []byte("value1.1")))
	require.False(t, batch.isDupDeleted(kv.AccountChangeSet, []byte("key1"), []byte("value-missing")))
	require.True(t, batch.isDupDeleted(kv.AccountChangeSet, []byte("key1"), []byte("value1.1")))
}

// AppendDup via the batch-level helper (distinct from the cursor-level call
// already exercised elsewhere).
func TestCov2BatchAppendDup(t *testing.T) {
	_, rwTx := memdb.NewTestTx(t)
	rwTx.Put(kv.AccountChangeSet, []byte("key1"), []byte("value1.1"))

	batch := NewMemoryBatch(rwTx, "", log.Root())
	defer batch.Close()

	require.NoError(t, batch.AppendDup(kv.AccountChangeSet, []byte("key1"), []byte("value1.2")))

	c, err := batch.CursorDupSort(kv.AccountChangeSet)
	require.NoError(t, err)
	defer c.Close()

	var got []string
	for _, v, err := c.First(); v != nil; _, v, err = c.Next() {
		require.NoError(t, err)
		got = append(got, string(v))
	}
	require.Equal(t, []string{"value1.1", "value1.2"}, got)
}
