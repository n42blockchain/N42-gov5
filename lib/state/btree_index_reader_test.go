package state

import (
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestBtIndexReader_LookupLookup2SeekEmpty(t *testing.T) {
	tmp := t.TempDir()
	logger := log.New()

	keyCount, M := 100, uint64(4)
	dataPath := generateCompressedKV(t, tmp, 52, 80, keyCount, logger)
	indexPath := path.Join(tmp, filepath.Base(dataPath)+".bti")
	require.NoError(t, BuildBtreeIndex(dataPath, indexPath, logger))

	bt, err := OpenBtreeIndex(indexPath, dataPath, M)
	require.NoError(t, err)
	defer bt.Close()

	require.False(t, bt.Empty())

	keys, err := pivotKeysFromKV(dataPath)
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	r := NewBtIndexReader(bt)
	require.False(t, r.Empty())

	off := r.Lookup(keys[0])
	_ = off

	off2 := r.Lookup2(keys[0][:20], keys[0][20:])
	_ = off2

	cur, err := r.Seek(keys[0])
	require.NoError(t, err)
	require.Equal(t, keys[0], cur.Key())

	// Nil-index reader: methods must degrade gracefully instead of panicking.
	var nilReader BtIndexReader
	require.EqualValues(t, 0, nilReader.Lookup(keys[0]))
	require.EqualValues(t, 0, nilReader.Lookup2(keys[0][:20], keys[0][20:]))
	_, err = nilReader.Seek(keys[0])
	require.Error(t, err)
}
