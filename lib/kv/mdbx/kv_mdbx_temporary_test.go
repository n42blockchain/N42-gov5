package mdbx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

// TestTemporaryMdbxFullAPI exercises every method on TemporaryMdbx, which are
// thin pass-through wrappers around the underlying in-memory MDBX instance.
func TestTemporaryMdbxFullAPI(t *testing.T) {
	ctx := context.Background()
	db, err := NewTemporaryMdbx(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(db.Close)

	require.False(t, db.ReadOnly())
	require.NotNil(t, db.CHandle())
	require.NotZero(t, db.PageSize())
	require.NotEmpty(t, db.AllTables())

	// Update
	err = db.Update(ctx, func(tx kv.RwTx) error {
		return tx.CreateBucket("TempTable")
	})
	require.NoError(t, err)

	// UpdateNosync
	err = db.UpdateNosync(ctx, func(tx kv.RwTx) error {
		return nil
	})
	require.NoError(t, err)

	// BeginRw / Commit
	rwTx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, rwTx.Put("TempTable", []byte("k"), []byte("v")))
	require.NoError(t, rwTx.Commit())

	// BeginRwNosync
	rwTx2, err := db.BeginRwNosync(ctx)
	require.NoError(t, err)
	rwTx2.Rollback()

	// View
	err = db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("TempTable", []byte("k"))
		require.NoError(t, err)
		require.Equal(t, []byte("v"), v)
		return nil
	})
	require.NoError(t, err)

	// BeginRo
	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	v, err := roTx.GetOne("TempTable", []byte("k"))
	require.NoError(t, err)
	require.Equal(t, []byte("v"), v)
	roTx.Rollback()
}

func TestNewTemporaryMdbxBadParentDir(t *testing.T) {
	// A parent dir that cannot exist (contains a NUL byte) should make
	// os.MkdirTemp fail, exercising the error branch of NewTemporaryMdbx.
	_, err := NewTemporaryMdbx(context.Background(), "/nonexistent-\x00-dir")
	require.Error(t, err)
}
