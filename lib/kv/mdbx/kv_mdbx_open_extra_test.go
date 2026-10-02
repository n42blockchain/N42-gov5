package mdbx

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

// TestBeginRoCanceledContext covers the ctx.Done() early-return branch of
// BeginRo.
func TestBeginRoCanceledContext(t *testing.T) {
	db := BaseCaseDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := db.BeginRo(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// TestBeginRwCanceledContext covers the same branch in beginRw (via BeginRw).
func TestBeginRwCanceledContext(t *testing.T) {
	db := BaseCaseDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := db.BeginRw(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// TestBeginRwNosync covers the TxNoSync flag path.
func TestBeginRwNosync(t *testing.T) {
	db := BaseCaseDB(t)
	tx, err := db.BeginRwNosync(context.Background())
	require.NoError(t, err)
	require.NoError(t, tx.Put("Table", []byte("k"), []byte("v")))
	require.NoError(t, tx.Commit())
}

// TestUpdateNosync covers the UpdateNosync convenience wrapper's success
// path.
func TestUpdateNosync(t *testing.T) {
	db := BaseCaseDB(t)
	err := db.UpdateNosync(context.Background(), func(tx kv.RwTx) error {
		return tx.Put("Table", []byte("k2"), []byte("v2"))
	})
	require.NoError(t, err)
}

// TestViewPropagatesCallbackError covers View()'s error pass-through.
func TestViewPropagatesCallbackError(t *testing.T) {
	db := BaseCaseDB(t)
	boom := errors.New("boom")
	err := db.View(context.Background(), func(tx kv.Tx) error {
		return boom
	})
	require.ErrorIs(t, err, boom)
}

// TestUpdatePropagatesCallbackErrorAndRollsBack covers update()'s rollback
// on a callback error, verifying no partial write survives.
func TestUpdatePropagatesCallbackErrorAndRollsBack(t *testing.T) {
	db := BaseCaseDB(t)
	boom := errors.New("boom")
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		_ = tx.Put("Table", []byte("rollback-me"), []byte("v"))
		return boom
	})
	require.ErrorIs(t, err, boom)

	err = db.View(context.Background(), func(tx kv.Tx) error {
		v, err := tx.GetOne("Table", []byte("rollback-me"))
		require.NoError(t, err)
		require.Nil(t, v)
		return nil
	})
	require.NoError(t, err)
}
