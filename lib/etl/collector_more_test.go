package etl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestMakeCurrentKeyStr(t *testing.T) {
	r := require.New(t)
	r.Equal("final", makeCurrentKeyStr(nil))
	r.Equal("01", makeCurrentKeyStr([]byte{1}))
	// >=8 bytes with leading zero prefix shows the full key.
	longZero := []byte{0, 0, 0, 0, 1, 2, 3, 4}
	r.Equal("0000000001020304", makeCurrentKeyStr(longZero))
	// long key without the zero-prefix pattern is truncated to 4 bytes.
	longKey := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	r.Equal("01020304", makeCurrentKeyStr(longKey))
}

func TestCollectorLogLvlAndBackgroundFlag(t *testing.T) {
	r := require.New(t)
	logger := log.New()
	c := NewCollector(t.Name(), "", NewSortableBuffer(1024), logger)
	defer c.Close()

	same := c.LogLvl(log.LvlWarn)
	r.Same(c, same)
	r.Equal(log.LvlWarn, c.logLvl)

	r.False(c.sortAndFlushInBackground)
	c.SortAndFlushInBackground(true)
	r.True(c.sortAndFlushInBackground)
}

func TestNewCriticalCollectorDoesNotAutoClean(t *testing.T) {
	r := require.New(t)
	logger := log.New()
	tmpdir := t.TempDir()
	c := NewCriticalCollector(t.Name(), tmpdir, NewSortableBuffer(1), logger)
	defer c.Close()
	r.False(c.autoClean)
}

func TestCollectorFlush(t *testing.T) {
	r := require.New(t)
	logger := log.New()
	tmpdir := t.TempDir()
	c := NewCollector(t.Name(), tmpdir, NewSortableBuffer(1024), logger)
	defer c.Close()

	// No data collected yet -> Flush is a no-op (allFlushed starts false but buf is empty).
	r.NoError(c.Flush())

	r.NoError(c.Collect([]byte("k1"), []byte("v1")))
	r.NoError(c.Flush())
	// Flush always goes through the disk-flush path (canStoreInRam=false), so
	// allFlushed stays false until Load's own flushBuffer(true) call runs.
	r.False(c.allFlushed)
	// A second flush is a no-op because the in-memory buffer was reset by the first.
	r.NoError(c.Flush())

	_, tx := memdb.NewTestTx(t)
	table := kv.ChaindataTables[0]
	r.NoError(c.Load(tx, table, IdentityLoadFunc, TransformArgs{}))
	v, err := tx.GetOne(table, []byte("k1"))
	r.NoError(err)
	r.Equal([]byte("v1"), v)
}

func TestNewCollectorFromFiles(t *testing.T) {
	r := require.New(t)

	// Non-existent directory -> nil, nil.
	c, err := NewCollectorFromFiles(t.Name(), filepath.Join(t.TempDir(), "missing"), log.New())
	r.NoError(err)
	r.Nil(c)

	// Empty directory -> nil, nil.
	emptyDir := t.TempDir()
	c, err = NewCollectorFromFiles(t.Name(), emptyDir, log.New())
	r.NoError(err)
	r.Nil(c)

	// Directory with leftover files from a previous flush is picked up.
	dataDir := t.TempDir()
	logger := log.New()
	orig := NewCollector(t.Name(), dataDir, NewSortableBuffer(1), logger)
	r.NoError(orig.Collect([]byte("a"), []byte("1")))
	r.NoError(orig.Collect([]byte("b"), []byte("2")))
	r.NoError(orig.Flush())
	// Leave the on-disk files in place (do not Close/cleanup).

	entries, err := os.ReadDir(dataDir)
	r.NoError(err)
	r.NotEmpty(entries)

	restored, err := NewCollectorFromFiles(t.Name(), dataDir, logger)
	r.NoError(err)
	r.NotNil(restored)
	r.True(restored.allFlushed)
	r.False(restored.autoClean)
	// NOTE: restored.Close()/Dispose() is deliberately not called here: the
	// providers built by NewCollectorFromFiles never set fileDataProvider.wg
	// (only FlushToDisk/FlushToDiskAsync do), so Dispose -> Wait() dereferences
	// a nil *errgroup.Group and panics. This is a latent defect in
	// NewCollectorFromFiles / fileDataProvider.Dispose, left unfixed per task
	// instructions (no non-test code changes). Reading via Next still works,
	// which is what we exercise here.
	data, _, err := restored.dataProviders[0].Next(nil, nil)
	r.NoError(err)
	r.NotNil(data)
}

func TestLoadMergedAndStreamMerged(t *testing.T) {
	r := require.New(t)
	logger := log.New()

	c1 := NewCollector(t.Name()+"-1", "", NewSortableBuffer(1024), logger)
	defer c1.Close()
	r.NoError(c1.Collect([]byte("a"), []byte("1")))
	r.NoError(c1.Collect([]byte("c"), []byte("3")))

	c2 := NewCollector(t.Name()+"-2", "", NewSortableBuffer(1024), logger)
	defer c2.Close()
	r.NoError(c2.Collect([]byte("b"), []byte("2")))
	r.NoError(c2.Collect([]byte("d"), []byte("4")))

	// LoadMerged with a nil collector in the list must be skipped gracefully.
	_, tx := memdb.NewTestTx(t)
	table := kv.ChaindataTables[0]
	r.NoError(LoadMerged(tx, table, IdentityLoadFunc, TransformArgs{}, c1, nil, c2))

	for k, v := range map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"} {
		got, err := tx.GetOne(table, []byte(k))
		r.NoError(err)
		r.Equal([]byte(v), got)
	}

	// StreamMerged over fresh collectors (data already consumed from c1/c2's
	// providers by LoadMerged, so build new ones for a clean stream test).
	s1 := NewCollector(t.Name()+"-s1", "", NewSortableBuffer(1024), logger)
	defer s1.Close()
	r.NoError(s1.Collect([]byte("x"), []byte("10")))
	s2 := NewCollector(t.Name()+"-s2", "", NewSortableBuffer(1024), logger)
	defer s2.Close()
	r.NoError(s2.Collect([]byte("y"), []byte("20")))

	var gotKeys []string
	r.NoError(StreamMerged(func(k, v []byte) error {
		gotKeys = append(gotKeys, string(k))
		return nil
	}, s1, s2))
	r.Equal([]string{"x", "y"}, gotKeys)

	// Empty provider set must return nil without panicking.
	r.NoError(LoadMerged(tx, table, IdentityLoadFunc, TransformArgs{}))
	r.NoError(StreamMerged(func(k, v []byte) error { return nil }))
}
