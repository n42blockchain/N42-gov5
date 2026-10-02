package mdbx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func autoDupSortDB(t *testing.T, table string) kv.RwDB {
	t.Helper()
	return NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			table: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                6,
				DupToLen:                  4,
			},
		}
	}).MustOpen()
}

// TestFirstDupLastDupNotFound covers the not-found branch of FirstDup/LastDup
// on an empty cursor (never positioned).
func TestFirstDupLastDupNotFound(t *testing.T) {
	_, _, c := BaseCase(t)
	// Position the cursor somewhere nonexistent so FirstDup/LastDup have no
	// current key to work from.
	k, v, err := c.SeekExact([]byte("does-not-exist"))
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)

	// Not positioned on any key: MDBX reports "cursor is not positioned to
	// data", which is not classified as IsNotFound, so both propagate it.
	_, err = c.FirstDup()
	require.Error(t, err)

	_, err = c.LastDup()
	require.Error(t, err)
}

// TestFirstDupLastDupFound covers the success branch for both.
func TestFirstDupLastDupFound(t *testing.T) {
	_, _, c := BaseCase(t)

	k, _, err := c.Seek([]byte("key1"))
	require.NoError(t, err)
	require.Equal(t, "key1", string(k))

	fv, err := c.FirstDup()
	require.NoError(t, err)
	require.Equal(t, "value1.1", string(fv))

	lv, err := c.LastDup()
	require.NoError(t, err)
	require.Equal(t, "value1.3", string(lv))
}

// TestDeleteDupSortFullKeyMismatchAndSuccess covers deleteDupSort's
// mismatched-value no-op branch and its successful delete-current branch,
// on the full (From-length) key path.
func TestDeleteDupSortFullKeyMismatchAndSuccess(t *testing.T) {
	const table = "ADS"
	db := autoDupSortDB(t, table)
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursorDupSort(table)
	require.NoError(t, err)
	defer c.Close()

	full := []byte{0, 0, 0, 1, 9, 9}
	require.NoError(t, c.Put(full, []byte("v")))

	// Mismatched sub-key: dup-key exists but with a different trailing
	// sub-key value, so delete is a silent no-op.
	mismatch := []byte{0, 0, 0, 1, 9, 8}
	require.NoError(t, c.Delete(mismatch))
	v, err := tx.GetOne(table, []byte{0, 0, 0, 1})
	require.NoError(t, err)
	require.NotNil(t, v, "row must still exist after a mismatched delete")

	// Exact match: deletes for real.
	require.NoError(t, c.Delete(full))
	_, vv, err := c.SeekBothExact([]byte{0, 0, 0, 1}, []byte{9, 9})
	require.NoError(t, err)
	require.Nil(t, vv)
}

// TestDeleteDupSortShortKeyNotFound covers deleteDupSort's len(key)<DupToLen
// path. The underlying DBI always stores exactly DupToLen-byte dup-keys for
// an AutoDupSortKeysConversion table, so a strictly-shorter key can never
// exact-match via set() — this exercises the IsNotFound no-op branch (the
// only reachable outcome for that length class; see final report).
func TestDeleteDupSortShortKeyNotFound(t *testing.T) {
	const table = "ADS2"
	db := autoDupSortDB(t, table)
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	c, err := tx.RwCursorDupSort(table)
	require.NoError(t, err)
	defer c.Close()

	full := []byte{0, 0, 0, 1, 9, 9}
	require.NoError(t, c.Put(full, []byte("v")))

	short := []byte{0, 0, 1}
	require.NoError(t, c.Delete(short))

	// The real row is untouched.
	v, err := tx.GetOne(table, []byte{0, 0, 0, 1})
	require.NoError(t, err)
	require.NotNil(t, v)
}
