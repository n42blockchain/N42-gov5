package mdbx

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

func TestPutNoOverwriteRejectsAutoDupSortConversion(t *testing.T) {
	cursor := &MdbxCursor{
		bucketCfg: kv.TableCfgItem{AutoDupSortKeysConversion: true},
	}

	err := cursor.PutNoOverwrite([]byte("k"), []byte("v"))
	if err == nil || !strings.Contains(err.Error(), "AutoDupSortKeysConversion") {
		t.Fatalf("PutNoOverwrite() error = %v, want AutoDupSortKeysConversion error", err)
	}
}

func TestAutoDupSortSeekPastLastDuplicate(t *testing.T) {
	const table = "AutoDupSort"
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
	t.Cleanup(tx.Rollback)
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	t.Cleanup(c.Close)

	firstPrefix := []byte{0, 0, 0, 1}
	secondPrefix := []byte{0, 0, 0, 2}
	secondKey := append(bytes.Clone(secondPrefix), 0, 1)
	require.NoError(t, c.Put(append(bytes.Clone(firstPrefix), 0, 1), []byte("first")))
	require.NoError(t, c.Put(secondKey, []byte("second")))

	k, v, err := c.Seek(append(bytes.Clone(firstPrefix), 0xff, 0xff))
	require.NoError(t, err)
	require.Equal(t, secondKey, k)
	require.Equal(t, []byte("second"), v)

	k, v, err = c.Seek(append(bytes.Clone(secondPrefix), 0xff, 0xff))
	require.NoError(t, err)
	require.Nil(t, k)
	require.Nil(t, v)
}

func TestTransactionPutWithCursor(t *testing.T) {
	const table = "CursorStats"
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{table: {}}
	}).MustOpen()
	t.Cleanup(db.Close)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	c, err := tx.RwCursor(table)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	concrete := tx.(*MdbxTx)
	require.Error(t, concrete.PutWithCursor(nil, []byte("key"), []byte("invalid")))
	// A transaction must not write through another transaction's cursor.
	foreign := &MdbxTx{}
	require.Error(t, foreign.PutWithCursor(c, []byte("key"), []byte("invalid")))
	count, size := concrete.writeCount.Load(), concrete.writeBytes.Load()
	require.NoError(t, concrete.PutWithCursor(c, []byte("key"), []byte("old")))
	_, _, err = c.Seek([]byte("key"))
	require.NoError(t, err)
	require.NoError(t, concrete.PutWithCursor(c, []byte("key"), []byte("longer value")))
	require.Equal(t, count+2, concrete.writeCount.Load())
	require.Equal(t, size+uint64(6+3+len("longer value")), concrete.writeBytes.Load())
	value, err := tx.GetOne(table, []byte("key"))
	require.NoError(t, err)
	require.Equal(t, []byte("longer value"), value)
	// An oversized key must fail without being counted as a write.
	require.Error(t, concrete.PutWithCursor(c, make([]byte, 1<<20), []byte("invalid")))
	require.Equal(t, count+2, concrete.writeCount.Load())
	require.Equal(t, size+uint64(6+3+len("longer value")), concrete.writeBytes.Load())
	if writeProbeEnabled {
		stat := concrete.tableWrites.rows[table]
		require.EqualValues(t, 2, stat.puts)
		require.EqualValues(t, 6+3+len("longer value"), stat.bytes)
	}
}
