package mdbx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestCursorFirstLastCurrentOnEmpty checks First/Last/Current/Next/Prev all
// return (nil, nil, nil) on an empty bucket, rather than erroring.
func TestCursorFirstLastCurrentOnEmpty(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursor("Table")
	require.NoError(t, err)
	defer c.Close()

	k, v, err := c.First()
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	k, v, err = c.Last()
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	// Current() on a cursor that was never positioned returns an error
	// (MDBX "cursor is not positioned to data"), unlike First/Next/Prev
	// which treat NotFound as an empty result.
	k, v, err = c.Current()
	require.Error(t, err)
	require.Empty(t, k)
	require.Nil(t, v)

	k, v, err = c.Next()
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	k, v, err = c.Prev()
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)
}

// TestCursorLastCurrentDeleteDeleteCurrent walks to the last entry, reads it
// via Current, then removes it two different ways (Delete by key and
// DeleteCurrent).
func TestCursorLastCurrentDeleteDeleteCurrent(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursor("Table")
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Put([]byte("a"), []byte("1")))
	require.NoError(t, c.Put([]byte("b"), []byte("2")))
	require.NoError(t, c.Put([]byte("c"), []byte("3")))

	k, v, err := c.Last()
	require.NoError(t, err)
	require.Equal(t, "c", string(k))
	require.Equal(t, "3", string(v))

	k, v, err = c.Current()
	require.NoError(t, err)
	require.Equal(t, "c", string(k))
	require.Equal(t, "3", string(v))

	require.NoError(t, c.DeleteCurrent())

	k, v, err = c.Last()
	require.NoError(t, err)
	require.Equal(t, "b", string(k))
	require.Equal(t, "2", string(v))

	require.NoError(t, c.Delete([]byte("a")))

	k, v, err = c.First()
	require.NoError(t, err)
	require.Equal(t, "b", string(k))
	require.Equal(t, "2", string(v))
}

// TestCursorSeekExact covers both the found and not-found paths of
// SeekExact on a plain (non-autodupsort) table.
func TestCursorSeekExact(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursor("Table")
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.Put([]byte("key"), []byte("val")))

	k, v, err := c.SeekExact([]byte("key"))
	require.NoError(t, err)
	require.Equal(t, "key", string(k))
	require.Equal(t, "val", string(v))

	k, v, err = c.SeekExact([]byte("missing"))
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)
}

// TestCursorSeekExactAutoDupSort covers SeekExact's AutoDupSortKeysConversion
// branch: both a composite key that resolves to a real row and one whose
// dup-suffix does not match (mismatched value -> nil result).
func TestCursorSeekExactAutoDupSort(t *testing.T) {
	const table = "AutoDupSort2"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			table: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                6,
				DupToLen:                  4,
			},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	defer c.Close()

	key := []byte{0, 0, 0, 1, 9, 9}
	require.NoError(t, c.Put(key, []byte("val")))

	k, v, err := c.SeekExact(key)
	require.NoError(t, err)
	require.Equal(t, key[:4], k)
	require.Equal(t, []byte("val"), v)

	// Not-found: no row under this prefix at all.
	missing := []byte{0, 0, 0, 2, 9, 9}
	k, v, err = c.SeekExact(missing)
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	// Found the dup-key but with a different trailing sub-key (mismatch).
	mismatch := []byte{0, 0, 0, 1, 9, 8}
	k, v, err = c.SeekExact(mismatch)
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)
}

// TestCursorUpsertPaths exercises Upsert on a plain DupSort (non-auto) table:
// insert-new, same-length-equal-noop, same-length-rewrite, and
// different-length-reset-all-dups branches of (*MdbxCursor).upsert.
func TestCursorUpsertPaths(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursorDupSort("Table")
	require.NoError(t, err)
	defer c.Close()
	mc := c.(*MdbxDupSortCursor)

	// insert-new (key not found at all)
	require.NoError(t, mc.Upsert([]byte("u1"), []byte("v1")))
	v, err := tx.GetOne("Table", []byte("u1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	// same-length, equal value: no-op branch
	require.NoError(t, mc.Upsert([]byte("u1"), []byte("v1")))

	// same-length, different value: in-place rewrite branch
	require.NoError(t, mc.Upsert([]byte("u1"), []byte("v2")))
	v, err = tx.GetOne("Table", []byte("u1"))
	require.NoError(t, err)
	require.Equal(t, []byte("v2"), v)

	// different length value: clear-all-dups-then-insert branch
	require.NoError(t, mc.Upsert([]byte("u1"), []byte("much-longer-value")))
	v, err = tx.GetOne("Table", []byte("u1"))
	require.NoError(t, err)
	require.Equal(t, []byte("much-longer-value"), v)

	// multiple dups under one key, then upsert collapses them to one value
	require.NoError(t, c.Put([]byte("u2"), []byte("d1")))
	require.NoError(t, c.Put([]byte("u2"), []byte("d2")))
	require.NoError(t, mc.Upsert([]byte("u2"), []byte("final")))
	cnt, err := c.CountDuplicates()
	require.NoError(t, err)
	require.Equal(t, uint64(1), cnt)
}

// TestCursorPutNoOverwriteRealEnv covers the success and KeyExists-rejection
// paths of PutNoOverwrite against a real MDBX cursor (not the unit test that
// only exercises the AutoDupSortKeysConversion guard).
func TestCursorPutNoOverwriteRealEnv(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursor("Table")
	require.NoError(t, err)
	defer c.Close()
	mc := c.(*MdbxDupSortCursor)

	require.NoError(t, mc.PutNoOverwrite([]byte("k"), []byte("v1")))
	err = mc.PutNoOverwrite([]byte("k"), []byte("v2"))
	require.Error(t, err)
}

// TestDupSortPrevNoDup walks to the end, then exercises PrevNoDup to jump
// across key groups (vs PrevDup, which stays within a group).
func TestDupSortPrevNoDup(t *testing.T) {
	_, _, c := BaseCase(t)

	k, v, err := c.Last()
	require.NoError(t, err)
	require.Equal(t, "key3", string(k))
	require.Equal(t, "value3.3", string(v))

	k, v, err = c.PrevNoDup()
	require.NoError(t, err)
	require.Equal(t, "key1", string(k))
	require.Equal(t, "value1.3", string(v))

	// Positioned at the very first group; PrevNoDup now finds nothing.
	k, v, err = c.PrevNoDup()
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)
}

// TestDupSortSeekBothExactAndDeleteExact covers SeekBothExact (found and
// not-found) and DeleteExact (found, then already-gone no-op).
func TestDupSortSeekBothExactAndDeleteExact(t *testing.T) {
	_, _, c := BaseCase(t)

	k, v, err := c.SeekBothExact([]byte("key1"), []byte("value1.1"))
	require.NoError(t, err)
	require.Equal(t, "key1", string(k))
	require.Equal(t, "value1.1", string(v))

	k, v, err = c.SeekBothExact([]byte("key1"), []byte("no-such-value"))
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))
	k, v, err = c.SeekBothExact([]byte("key1"), []byte("value1.1"))
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	// Deleting again is a no-op, not an error.
	require.NoError(t, c.DeleteExact([]byte("key1"), []byte("value1.1")))
}

// TestDupSortAppendAndAppendDup covers the dup-sort cursor's fast
// append-only insertion paths.
func TestDupSortAppendAndAppendDup(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursorDupSort("Table")
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Append([]byte("a"), []byte("1")))
	require.NoError(t, c.Append([]byte("b"), []byte("1")))
	require.NoError(t, c.AppendDup([]byte("b"), []byte("2")))

	cnt, err := c.CountDuplicates()
	require.NoError(t, err)
	require.Equal(t, uint64(2), cnt)
}

// TestDupSortPutNoDupDataAndDeleteCurrentDuplicates covers PutNoDupData
// (rejecting an exact duplicate) and DeleteCurrentDuplicates (removing
// every value under the current key).
func TestDupSortPutNoDupDataAndDeleteCurrentDuplicates(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursorDupSort("Table")
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.PutNoDupData([]byte("k"), []byte("v1")))
	require.NoError(t, c.PutNoDupData([]byte("k"), []byte("v2")))
	// exact duplicate: rejected by MDBX's NoDupData flag (KeyExist error).
	err = c.PutNoDupData([]byte("k"), []byte("v1"))
	require.Error(t, err)

	cnt, err := c.CountDuplicates()
	require.NoError(t, err)
	require.Equal(t, uint64(2), cnt)

	require.NoError(t, c.DeleteCurrentDuplicates())
	cnt, err = c.CountDuplicates()
	require.NoError(t, err)
	require.Equal(t, uint64(0), cnt)
}

// TestDupSortInternal covers the *MdbxDupSortCursor.Internal accessor.
func TestDupSortInternal(t *testing.T) {
	_, _, c := BaseCase(t)
	dc := c.(*MdbxDupSortCursor)
	require.NotNil(t, dc.Internal())
}

// TestCursorDeleteDupSortBuckets covers Delete() on a plain (non-auto)
// DupSort bucket, which must clear all duplicates for the key.
func TestCursorDeleteDupSortBucket(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	c, err := tx.RwCursor("Table")
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Put([]byte("multi"), []byte("v1")))
	require.NoError(t, c.(kv.RwCursorDupSort).PutNoDupData([]byte("multi"), []byte("v2")))

	require.NoError(t, c.Delete([]byte("multi")))

	v, err := tx.GetOne("Table", []byte("multi"))
	require.NoError(t, err)
	require.Nil(t, v)

	// Deleting a missing key is a no-op.
	require.NoError(t, c.Delete([]byte("missing")))
}

// TestCursorAppendAutoDupSortBadKeyLen covers the Append() length-validation
// error branch on an AutoDupSortKeysConversion bucket.
func TestCursorAppendAutoDupSortBadKeyLen(t *testing.T) {
	const table = "AutoDupSort3"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			table: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                6,
				DupToLen:                  4,
			},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	defer c.Close()

	// len(k) == 5: not equal to DupFromLen(6), and >= DupToLen(4) -> error.
	err = c.Append([]byte{1, 2, 3, 4, 5}, []byte("v"))
	require.Error(t, err)
}

// TestCursorDeleteDupSortBadKeyLen covers deleteDupSort's length-validation
// error branch.
func TestCursorDeleteDupSortBadKeyLen(t *testing.T) {
	const table = "AutoDupSort4"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			table: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                6,
				DupToLen:                  4,
			},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	defer c.Close()

	err = c.Delete([]byte{1, 2, 3, 4, 5})
	require.Error(t, err)
}

// TestCursorPutDupSortBadKeyLen covers putDupSort's length-validation error.
func TestCursorPutDupSortBadKeyLen(t *testing.T) {
	const table = "AutoDupSort5"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			table: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                6,
				DupToLen:                  4,
			},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	defer c.Close()

	err = c.Put([]byte{1, 2, 3, 4, 5}, []byte("v"))
	require.Error(t, err)
}
