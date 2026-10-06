package state

import (
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestBtIndexCursorOrdinalAndNext drives the Cursor accessor methods that
// NewBtIndexReader's own tests never call directly: Ordinal() and Next()
// walking from the first key to the second.
func TestBtIndexCursorOrdinalAndNext(t *testing.T) {
	tmp := t.TempDir()
	logger := log.New()

	keyCount, M := 100, uint64(4)
	dataPath := generateCompressedKV(t, tmp, 52, 80, keyCount, logger)
	indexPath := path.Join(tmp, filepath.Base(dataPath)+".bti")
	require.NoError(t, BuildBtreeIndex(dataPath, indexPath, logger))

	bt, err := OpenBtreeIndex(indexPath, dataPath, M)
	require.NoError(t, err)
	defer bt.Close()

	keys, err := pivotKeysFromKV(dataPath)
	require.NoError(t, err)
	require.True(t, len(keys) >= 2)

	cur, err := bt.Seek(keys[0])
	require.NoError(t, err)
	require.Equal(t, keys[0], cur.Key())
	firstOrdinal := cur.Ordinal()

	require.True(t, cur.Next())
	require.EqualValues(t, firstOrdinal+1, cur.Ordinal())

	// Advance to the very last ordinal, then Next() must report false.
	last, err := bt.Seek(keys[len(keys)-1])
	require.NoError(t, err)
	for last.Next() {
	}
}
