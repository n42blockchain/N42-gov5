package mdbx

import (
	"context"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestMdbxOptsBuilderChain exercises every chainable MdbxOpts builder method
// and getter so that both the fluent setters and the accessors are covered.
func TestMdbxOptsBuilderChain(t *testing.T) {
	logger := log.New()
	sem := semaphore.NewWeighted(10)

	opts := NewMDBX(logger).
		Label(kv.ChainDB).
		DirtySpace(1024).
		RoTxsLimiter(sem).
		PageSize(kv.DefaultPageSize()).
		GrowthStep(1 * datasize.MB).
		Exclusive().
		Flags(func(f uint) uint { return f | mdbx.NoReadahead }).
		SyncPeriod(time.Second).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		MapSize(64 * datasize.MB).
		WriteMap().
		SafeNoSync().
		LifoReclaim().
		RpAugmentLimit(1024).
		WriteMergeThreshold(8192)

	require.Equal(t, kv.ChainDB, opts.GetLabel())
	require.False(t, opts.GetInMem())
	require.Equal(t, kv.DefaultPageSize(), opts.GetPageSize())
	require.True(t, opts.HasFlag(mdbx.NoReadahead))
	require.True(t, opts.HasFlag(mdbx.Exclusive))
	require.True(t, opts.HasFlag(mdbx.WriteMap))
	require.True(t, opts.HasFlag(mdbx.SafeNoSync))
	require.True(t, opts.HasFlag(mdbx.LifoReclaim))

	// Set() just returns the argument opts, exercised for coverage.
	same := opts.Set(opts)
	require.Equal(t, opts.GetLabel(), same.GetLabel())
}

// TestMdbxOptsAccedeWithoutExistingDB verifies Accede()+Open() returns
// ErrDBDoesNotExists when no database exists yet at the given path.
func TestMdbxOptsAccedeWithoutExistingDB(t *testing.T) {
	dir := t.TempDir() + "/does-not-exist-yet"
	_, err := NewMDBX(log.New()).Path(dir).Accede().Open(context.Background())
	require.Error(t, err)
}

// TestMdbxOptsReadonlyFlag covers the Readonly() builder via a round trip:
// create a DB read-write, then reopen read-only against the same path.
func TestMdbxOptsReadonlyFlag(t *testing.T) {
	path := t.TempDir()
	rw := NewMDBX(log.New()).Path(path).MapSize(16 * datasize.MB).MustOpen()
	rw.Close()

	ro, err := NewMDBX(log.New()).Path(path).Readonly().Open(context.Background())
	require.NoError(t, err)
	t.Cleanup(ro.Close)
	require.True(t, ro.ReadOnly())
}

// TestUtilMustOpenAndOpen covers the package-level MustOpen/Open convenience
// wrappers in util.go.
func TestUtilMustOpenAndOpen(t *testing.T) {
	path := t.TempDir()
	db := MustOpen(path)
	defer db.Close()
	require.False(t, db.ReadOnly())

	ctx := context.Background()
	db2, err := Open(ctx, t.TempDir(), log.New(), false)
	require.NoError(t, err)
	defer db2.Close()
	require.NotNil(t, db2)
}

func TestUtilOpenAccede(t *testing.T) {
	path := t.TempDir()
	// Create a DB first, so Accede has something to attach to.
	db := MustOpen(path)
	db.Close()

	db2, err := Open(context.Background(), path, log.New(), true)
	require.NoError(t, err)
	defer db2.Close()
}

// TestMdbxOptsPathDbMap verifies that opening a DB registers its path in the
// global PathDbMap, and closing removes it.
func TestMdbxOptsPathDbMap(t *testing.T) {
	path := t.TempDir()
	db := MustOpen(path)
	found := false
	for p := range PathDbMap() {
		if p == path {
			found = true
		}
	}
	require.True(t, found, "expected %s to be registered in PathDbMap", path)
	db.Close()

	for p := range PathDbMap() {
		require.NotEqual(t, path, p)
	}
}
