// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_builder_cs_test.go drives HistoryBuilder.BuildFromChangesets end
// to end — detectCSTable's probing, collectFromChangesets, buildSegment and
// SegmentStoreWriter — against the same synthetic AccountChangeSet /
// StorageChangeSet DupSort tables used by account_cs_run_test.go /
// storage_cs_run_test.go, then reads the result back with NewHistoryReader.

package cscompact

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHistoryBuilder_BuildFromChangesets_Account exercises the account-side
// path: detectCSTable must pick "AccountChangeSet" (the Erigon name; the
// Reth "AccountChangeSets" table doesn't exist in this fixture).
func TestHistoryBuilder_BuildFromChangesets_Account(t *testing.T) {
	db := csOpenChangesetDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutAccountChange(t, tx, 0, csAddr(0x11), []byte{1})
	csPutAccountChange(t, tx, 1, csAddr(0x11), []byte{2})
	csPutAccountChange(t, tx, 2, csAddr(0x22), nil)
	require.NoError(t, tx.Commit())

	outDir := t.TempDir()
	b := NewAccountHistoryBuilder(db, outDir)
	require.Equal(t, "AccountChangeSet", b.detectCSTable(ctx))

	require.NoError(t, b.BuildFromChangesets(ctx, 0, 3))

	reader, err := NewHistoryReader(outDir, "accthist")
	require.NoError(t, err)
	defer reader.Close()
	require.EqualValues(t, 1, reader.SegmentCount())

	addr := csAddr(0x11)
	found, ok := reader.Lookup(addr[:], 1)
	require.True(t, ok)
	require.EqualValues(t, 1, found)
}

// TestHistoryBuilder_BuildFromChangesets_Storage exercises the storage-side
// path (keyLen=52, composite addr+slot key) against the AutoDupSortKeys-
// Conversion StorageChangeSet fixture.
func TestHistoryBuilder_BuildFromChangesets_Storage(t *testing.T) {
	db := csOpenStorageChangesetDB(t)
	ctx := context.Background()

	addr := csAddr(0x33)
	var slot [32]byte
	slot[31] = 7

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutStorageChange(t, tx, 0, addr, 1, slot, []byte{1})
	csPutStorageChange(t, tx, 1, addr, 1, slot, []byte{2})
	require.NoError(t, tx.Commit())

	outDir := t.TempDir()
	b := NewStorageHistoryBuilder(db, outDir)
	require.Equal(t, "StorageChangeSet", b.detectCSTable(ctx))

	require.NoError(t, b.BuildFromChangesets(ctx, 0, 2))

	reader, err := NewHistoryReader(outDir, "storhist")
	require.NoError(t, err)
	defer reader.Close()
	require.EqualValues(t, 1, reader.SegmentCount())

	compositeKey := append(append([]byte{}, addr[:]...), slot[:]...)
	found, ok := reader.Lookup(compositeKey, 1)
	require.True(t, ok)
	require.EqualValues(t, 1, found)
}
