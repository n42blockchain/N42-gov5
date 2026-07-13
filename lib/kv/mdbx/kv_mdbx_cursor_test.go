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
