// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestCompareMDBXTables_IdenticalIsOK seeds two identical databases and
// confirms both Account and Storage compare OK with no mismatches.
func TestCompareMDBXTables_IdenticalIsOK(t *testing.T) {
	dbA := memdb.NewTestDB(t)
	dbB := memdb.NewTestDB(t)
	ctx := context.Background()

	txA, err := dbA.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, txA.Put(modules.Account, []byte{1}, []byte{0xAA}))
	require.NoError(t, txA.Put(modules.Storage, []byte{1}, []byte{0xBB}))
	require.NoError(t, txA.Commit())

	txB, err := dbB.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, txB.Put(modules.Account, []byte{1}, []byte{0xAA}))
	require.NoError(t, txB.Put(modules.Storage, []byte{1}, []byte{0xBB}))
	require.NoError(t, txB.Commit())

	roA, err := dbA.BeginRo(ctx)
	require.NoError(t, err)
	defer roA.Rollback()
	roB, err := dbB.BeginRo(ctx)
	require.NoError(t, err)
	defer roB.Rollback()

	results, err := CompareMDBXTables(roA, roB, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, r := range results {
		require.True(t, r.OK, "table %s expected OK", r.Table)
		require.Zero(t, r.Mismatches)
		require.Zero(t, r.OnlyInA)
		require.Zero(t, r.OnlyInB)
	}
}

// TestCompareMDBXTables_DetectsValueMismatchAndKeyDrift exercises all three
// divergence branches: differing value for the same key, a key only in A,
// and a key only in B.
func TestCompareMDBXTables_DetectsValueMismatchAndKeyDrift(t *testing.T) {
	dbA := memdb.NewTestDB(t)
	dbB := memdb.NewTestDB(t)
	ctx := context.Background()

	txA, err := dbA.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, txA.Put(modules.Account, []byte{1}, []byte{0xAA})) // mismatch value
	require.NoError(t, txA.Put(modules.Account, []byte{2}, []byte{0xCC})) // only in A
	require.NoError(t, txA.Commit())

	txB, err := dbB.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, txB.Put(modules.Account, []byte{1}, []byte{0xAB})) // mismatch value
	require.NoError(t, txB.Put(modules.Account, []byte{3}, []byte{0xDD})) // only in B
	require.NoError(t, txB.Commit())

	roA, err := dbA.BeginRo(ctx)
	require.NoError(t, err)
	defer roA.Rollback()
	roB, err := dbB.BeginRo(ctx)
	require.NoError(t, err)
	defer roB.Rollback()

	results, err := CompareMDBXTables(roA, roB, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	acctRes := results[0]
	require.Equal(t, modules.Account, acctRes.Table)
	require.False(t, acctRes.OK)
	require.Equal(t, 1, acctRes.Mismatches)
	require.Equal(t, 1, acctRes.OnlyInA)
	require.Equal(t, 1, acctRes.OnlyInB)
	require.NotEmpty(t, acctRes.FirstDiff)

	// Storage was never touched in either DB — must still report OK.
	stoRes := results[1]
	require.Equal(t, modules.Storage, stoRes.Table)
	require.True(t, stoRes.OK)
}
