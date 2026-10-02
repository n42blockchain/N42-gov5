// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules"
)

// TestCloneState_CopiesTablesAndDbInfo drives CloneState end to end against
// two in-memory MDBX instances: seed Account/Storage/Code/DbInfo rows in the
// source, clone into an empty target, and verify all rows + the progress
// marker made it across.
func TestCloneState_CopiesTablesAndDbInfo(t *testing.T) {
	srcDB := newDictTestDB(t)
	dstDB := newDictTestDB(t)
	ctx := context.Background()

	// Seed source tables directly.
	tx, err := srcDB.BeginRw(ctx)
	require.NoError(t, err)
	for i := byte(0); i < 5; i++ {
		require.NoError(t, tx.Put(modules.Account, []byte{i}, []byte{i, i}))
		require.NoError(t, tx.Put(modules.Storage, []byte{i}, []byte{i, i, i}))
		require.NoError(t, tx.Put(modules.Code, []byte{i}, []byte{i}))
	}
	require.NoError(t, tx.Put("DbInfo", []byte("ethel_progress"), []byte{0xAA}))
	require.NoError(t, tx.Commit())

	require.NoError(t, CloneState(ctx, srcDB, dstDB, CloneStateOptions{BatchN: 2}))

	checkTx, err := dstDB.BeginRo(ctx)
	require.NoError(t, err)
	defer checkTx.Rollback()

	for i := byte(0); i < 5; i++ {
		v, err := checkTx.GetOne(modules.Account, []byte{i})
		require.NoError(t, err)
		require.Equal(t, []byte{i, i}, v)

		v, err = checkTx.GetOne(modules.Storage, []byte{i})
		require.NoError(t, err)
		require.Equal(t, []byte{i, i, i}, v)

		v, err = checkTx.GetOne(modules.Code, []byte{i})
		require.NoError(t, err)
		require.Equal(t, []byte{i}, v)
	}

	progress, err := checkTx.GetOne("DbInfo", []byte("ethel_progress"))
	require.NoError(t, err)
	require.Equal(t, []byte{0xAA}, progress)
}

// TestCloneState_EmptySourceIsNoop confirms cloning an empty source produces
// no error and copies zero rows.
func TestCloneState_EmptySourceIsNoop(t *testing.T) {
	srcDB := newDictTestDB(t)
	dstDB := newDictTestDB(t)
	ctx := context.Background()

	require.NoError(t, CloneState(ctx, srcDB, dstDB, CloneStateOptions{}))

	checkTx, err := dstDB.BeginRo(ctx)
	require.NoError(t, err)
	defer checkTx.Rollback()

	cur, err := checkTx.Cursor(modules.Account)
	require.NoError(t, err)
	defer cur.Close()
	k, _, err := cur.First()
	require.NoError(t, err)
	require.Nil(t, k)
}
