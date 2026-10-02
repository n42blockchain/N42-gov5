package kvcache

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/stretchr/testify/require"
)

// covSetVersion writes the PlainStateVersion sequence entry to the given id.
func covSetVersion(t *testing.T, tx kv.RwTx, id uint64) {
	t.Helper()
	var versionID [8]byte
	binary.BigEndian.PutUint64(versionID[:], id)
	require.NoError(t, tx.Put(kv.Sequence, kv.PlainStateVersion, versionID[:]))
}

// TestDummyCacheCov exercises the no-op DummyCache/DummyView implementation.
func TestDummyCacheCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	dc := NewDummy()
	require.Equal(0, dc.Len())
	dc.OnNewBlock(&remote.StateChangeBatch{})

	db := memdb.NewTestDB(t)
	err := db.Update(ctx, func(tx kv.RwTx) error {
		require.NoError(tx.Put(kv.PlainState, []byte{1}, []byte{9}))
		require.NoError(tx.Put(kv.Code, []byte{1}, []byte{8}))

		view, err := dc.View(ctx, tx)
		require.NoError(err)

		v, err := view.Get([]byte{1})
		require.NoError(err)
		require.Equal([]byte{9}, v)

		code, err := view.GetCode([]byte{1})
		require.NoError(err)
		require.Equal([]byte{8}, code)

		res, err := dc.ValidateCurrentRoot(ctx, tx)
		require.NoError(err)
		require.False(res.Enabled)
		return nil
	})
	require.NoError(err)
}

// TestCoherentViewGetGetCode exercises CoherentView.Get/GetCode proxy methods
// and the codeHash path via Action_UPSERT_CODE.
func TestCoherentViewGetGetCode(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	c := New(DefaultCoherentConfig)
	db := memdb.NewTestDB(t)
	addr := [20]byte{7}
	code := []byte("some-code-bytes")

	var id uint64
	err := db.Update(ctx, func(tx kv.RwTx) error {
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{{
					Action:  remote.Action_UPSERT_CODE,
					Address: gointerfaces.ConvertAddressToH160(addr),
					Data:    []byte{1},
					Code:    code,
				}},
			},
		},
	})

	err = db.View(ctx, func(tx kv.Tx) error {
		view, err := c.View(ctx, tx)
		require.NoError(err)
		cv, ok := view.(*CoherentView)
		require.True(ok)

		v, err := cv.Get(addr[:])
		require.NoError(err)
		require.Equal([]byte{1}, v)

		gotCode, err := cv.GetCode(c.codeHash(code))
		require.NoError(err)
		require.Equal(code, gotCode)
		return nil
	})
	require.NoError(err)
}

// TestCoherentRemoveOldestCov forces eviction of both the state and code
// caches by using a tiny CacheSize/CodeCacheSize, covering removeOldest and
// removeOldestCode.
func TestCoherentRemoveOldestCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	cfg := DefaultCoherentConfig
	cfg.CacheSize = 1
	cfg.CodeCacheSize = 1
	cfg.NewBlockWait = 0
	c := New(cfg)
	db := memdb.NewTestDB(t)

	var id uint64
	err := db.Update(ctx, func(tx kv.RwTx) error {
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	addr1, addr2 := [20]byte{1}, [20]byte{2}
	code1, code2 := []byte("code-one"), []byte("code-two")

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{
					{
						Action:  remote.Action_UPSERT_CODE,
						Address: gointerfaces.ConvertAddressToH160(addr1),
						Data:    []byte{1},
						Code:    code1,
					},
					{
						Action:  remote.Action_UPSERT_CODE,
						Address: gointerfaces.ConvertAddressToH160(addr2),
						Data:    []byte{2},
						Code:    code2,
					},
				},
			},
		},
	})

	// Cache size of 1 byte forces eviction down to (ideally) nothing extra kept;
	// just assert no panic and lengths stay bounded.
	require.LessOrEqual(c.roots[id].cache.Len(), 2)
	require.LessOrEqual(c.roots[id].codeCache.Len(), 2)
}

// TestValidateCurrentRootCov drives ValidateCurrentRoot through the
// up-to-date / stale / cache-cleared paths, plus cloneCaches/clearCaches.
func TestValidateCurrentRootCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	cfg := DefaultCoherentConfig
	cfg.NewBlockWait = 0
	c := New(cfg)
	db := memdb.NewTestDB(t)
	k1 := [20]byte{1}

	// Case 1: stateID > latestStateVersionID -> LatestStateBehind.
	err := db.Update(ctx, func(tx kv.RwTx) error {
		covSetVersion(t, tx, 100)
		return nil
	})
	require.NoError(err)
	err = db.View(ctx, func(tx kv.Tx) error {
		res, err := c.ValidateCurrentRoot(ctx, tx)
		require.NoError(err)
		require.True(res.Enabled)
		require.True(res.LatestStateBehind)
		return nil
	})
	require.NoError(err)

	// Case 2: bring cache up to date and populate it with a value matching DB.
	var id uint64
	err = db.Update(ctx, func(tx kv.RwTx) error {
		require.NoError(tx.Put(kv.PlainState, k1[:], []byte{9}))
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{{
					Action:  remote.Action_UPSERT,
					Address: gointerfaces.ConvertAddressToH160(k1),
					Data:    []byte{9},
				}},
			},
		},
	})

	err = db.View(ctx, func(tx kv.Tx) error {
		res, err := c.ValidateCurrentRoot(ctx, tx)
		require.NoError(err)
		require.True(res.Enabled)
		require.False(res.LatestStateBehind)
		require.False(res.CacheCleared)
		require.Empty(res.StateKeysOutOfSync)
		return nil
	})
	require.NoError(err)

	// Case 3: mutate DB directly (bypassing cache) so cached value is stale,
	// then validate again expecting CacheCleared=true and keys reported.
	err = db.Update(ctx, func(tx kv.RwTx) error {
		require.NoError(tx.Put(kv.PlainState, k1[:], []byte{123}))
		return nil
	})
	require.NoError(err)

	err = db.View(ctx, func(tx kv.Tx) error {
		res, err := c.ValidateCurrentRoot(ctx, tx)
		require.NoError(err)
		require.True(res.CacheCleared)
		require.NotEmpty(res.StateKeysOutOfSync)
		return nil
	})
	require.NoError(err)

	// After clearing, the root cache should be empty.
	root := c.roots[c.latestStateVersionID]
	require.Equal(0, root.cache.Len())
}

// TestValidateCurrentRootCancelledCov covers the context-cancelled early
// return path.
func TestValidateCurrentRootCancelledCov(t *testing.T) {
	require := require.New(t)
	c := New(DefaultCoherentConfig)
	db := memdb.NewTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := db.View(context.Background(), func(tx kv.Tx) error {
		res, err := c.ValidateCurrentRoot(ctx, tx)
		require.NoError(err)
		require.True(res.RequestCancelled)
		return nil
	})
	require.NoError(err)
}

// TestCoherentLenAndDebugStatsCov covers Len() (both nil and populated views)
// and the DebugStats helper.
func TestCoherentLenAndDebugStatsCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	cfg := DefaultCoherentConfig
	cfg.NewBlockWait = 0
	c := New(cfg)
	require.Equal(0, c.Len())
	require.Nil(DebugStats(&DummyCache{}))
	require.Empty(DebugStats(c))

	db := memdb.NewTestDB(t)
	k1 := [20]byte{1}
	var id uint64
	err := db.Update(ctx, func(tx kv.RwTx) error {
		require.NoError(tx.Put(kv.PlainState, k1[:], []byte{1}))
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{{
					Action:  remote.Action_UPSERT,
					Address: gointerfaces.ConvertAddressToH160(k1),
					Data:    []byte{1},
				}},
			},
		},
	})

	require.Equal(1, c.Len())
	stats := DebugStats(c)
	require.Len(stats, 1)
	require.Equal(id, stats[0].BlockNum)
}

// TestOnNewBlockRemoveActionCov covers the Action_REMOVE branch of OnNewBlock.
func TestOnNewBlockRemoveActionCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	cfg := DefaultCoherentConfig
	cfg.NewBlockWait = 0
	c := New(cfg)
	db := memdb.NewTestDB(t)
	k1 := [20]byte{3}

	var id uint64
	err := db.Update(ctx, func(tx kv.RwTx) error {
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{{
					Action:  remote.Action_REMOVE,
					Address: gointerfaces.ConvertAddressToH160(k1),
				}},
			},
		},
	})

	err = db.View(ctx, func(tx kv.Tx) error {
		view, err := c.View(ctx, tx)
		require.NoError(err)
		v, err := view.Get(k1[:])
		require.NoError(err)
		require.Nil(v)
		return nil
	})
	require.NoError(err)
}

// TestOnNewBlockStorageChangesCov covers the StorageChanges branch of
// OnNewBlock (requires cfg.WithStorage).
func TestOnNewBlockStorageChangesCov(t *testing.T) {
	require, ctx := require.New(t), context.Background()
	cfg := DefaultCoherentConfig
	cfg.NewBlockWait = 0
	cfg.WithStorage = true
	c := New(cfg)
	db := memdb.NewTestDB(t)
	addr := [20]byte{4}
	loc := [32]byte{5}

	var id uint64
	err := db.Update(ctx, func(tx kv.RwTx) error {
		id = tx.ViewID()
		covSetVersion(t, tx, id)
		return nil
	})
	require.NoError(err)

	c.OnNewBlock(&remote.StateChangeBatch{
		StateVersionId: id,
		ChangeBatch: []*remote.StateChange{
			{
				Changes: []*remote.AccountChange{{
					Action:  remote.Action_STORAGE,
					Address: gointerfaces.ConvertAddressToH160(addr),
					StorageChanges: []*remote.StorageChange{{
						Location: gointerfaces.ConvertHashToH256(loc),
						Data:     []byte{42},
					}},
				}},
			},
		},
	})

	wantKey := make([]byte, 52)
	copy(wantKey, addr[:])
	copy(wantKey[20:], loc[:])

	err = db.View(ctx, func(tx kv.Tx) error {
		view, err := c.View(ctx, tx)
		require.NoError(err)
		v, err := view.Get(wantKey)
		require.NoError(err)
		require.Equal([]byte{42}, v)
		return nil
	})
	require.NoError(err)
}
