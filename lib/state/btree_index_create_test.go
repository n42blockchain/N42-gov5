package state

import (
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestCreateBtreeIndex(t *testing.T) {
	tmp := t.TempDir()
	logger := log.New()

	keyCount, M := 50, uint64(4)
	dataPath := generateCompressedKV(t, tmp, 52, 60, keyCount, logger)
	indexPath := path.Join(tmp, filepath.Base(dataPath)+".bti")

	bt, err := CreateBtreeIndex(indexPath, dataPath, M, logger)
	require.NoError(t, err)
	defer bt.Close()

	require.EqualValues(t, keyCount, bt.KeyCount())
	require.False(t, bt.Empty())
	require.Greater(t, bt.Size(), int64(0))
	require.False(t, bt.ModTime().IsZero())
	require.NotEmpty(t, bt.FilePath())
	require.NotEmpty(t, bt.FileName())

	keys, err := pivotKeysFromKV(dataPath)
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	cur, err := bt.Seek(keys[0])
	require.NoError(t, err)
	require.Equal(t, keys[0], cur.Key())

	off := bt.Lookup(keys[0])
	_ = off

	c := bt.OrdinalLookup(0)
	require.NotNil(t, c)
}

func TestBtIndexWriter_DisableFsync(t *testing.T) {
	tmp := t.TempDir()
	logger := log.New()
	args := BtIndexWriterArgs{
		IndexFile: path.Join(tmp, "idx.bt"),
		TmpDir:    tmp,
		KeyCount:  4,
	}
	iw, err := NewBtIndexWriter(args, logger)
	require.NoError(t, err)
	defer iw.Close()

	iw.DisableFsync()
	require.True(t, iw.noFsync)
	require.NoError(t, iw.fsync())
}

func TestBtIndexCloseAndEmptyNilSafe(t *testing.T) {
	var bt *BtIndex
	require.True(t, bt.Empty())
	bt.Close() // nil receiver: must not panic
}
