// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_edge_test.go rounds out small branch gaps across the five
// files in scope: cacheHeader's eviction, truncHex's truncation branch,
// alignCSTable's unfillable-gap error, ApplyChangesetsToMDBX's open and
// decode-error paths, readHeader's not-found error, and fileSize's
// stat-error path.

package ethel

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// TestExecutorCacheHeader_Evicts confirms cacheHeader evicts the entry
// 256 blocks behind the one just cached.
func TestExecutorCacheHeader_Evicts(t *testing.T) {
	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, nil)

	h0 := mkHeader(0, tsAnchor, types.Hash{}, types.Hash{}, types.Hash{})
	executor.cacheHeader(0, h0)
	_, ok := executor.headerCache[0]
	require.True(t, ok)

	h256 := mkHeader(256, tsAnchor+256, types.Hash{}, types.Hash{}, types.Hash{})
	executor.cacheHeader(256, h256)
	_, stillThere := executor.headerCache[0]
	require.False(t, stillThere, "cacheHeader must evict num-256 once num>=256")
	_, newEntry := executor.headerCache[256]
	require.True(t, newEntry)
}

// TestTruncHex covers both the pass-through (len<=max) and truncation
// (len>max) branches.
func TestTruncHex(t *testing.T) {
	short := []byte{1, 2, 3}
	require.Equal(t, short, truncHex(short, 8))

	long := make([]byte, 40)
	for i := range long {
		long[i] = byte(i)
	}
	got := truncHex(long, 8)
	require.Len(t, got, 8)
	require.Equal(t, long[:8], got)
}

// TestAlignCSTable_UnfillableGap covers the default branch: a table whose
// entries end strictly below the wanted start, which the sink refuses to
// paper over.
func TestAlignCSTable_UnfillableGap(t *testing.T) {
	fz := mkFreezer(t)
	tbl, err := fz.EnsureTable(freezer.TableAccountChanges, "c")
	require.NoError(t, err)
	require.NoError(t, tbl.SetStartItem(5))
	require.NoError(t, tbl.Append(5, []byte{1}))
	require.NoError(t, tbl.Append(6, []byte{2}))
	// Items()==7 now; want=20 is far ahead -> unfillable gap.
	err = alignCSTable(tbl, 20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gap")
}

// TestApplyChangesetsToMDBX_OpenError covers the "open acctcs" error path
// when leavesDir has no acctcs table at all.
func TestApplyChangesetsToMDBX_OpenError(t *testing.T) {
	db := memdb.NewTestDB(t)
	err := ApplyChangesetsToMDBX(context.Background(), db, t.TempDir(), 0, 1)
	require.Error(t, err)
}

// TestApplyChangesetsToMDBX_DecodeError covers the decode-failure path:
// a batch-compressed table whose entry is not a valid changeset blob.
func TestApplyChangesetsToMDBX_DecodeError(t *testing.T) {
	dir := t.TempDir()
	fz, err := freezer.New(dir, freezer.DefaultFreezeThreshold)
	require.NoError(t, err)
	batcher, err := newOutputBatcher(fz)
	require.NoError(t, err)
	// Garbage account-change blob, valid (empty) storage blob.
	require.NoError(t, batcher.addEntry(freezer.TableAccountChanges, "c", []byte{0xff, 0xff, 0xff}))
	require.NoError(t, batcher.addEntry(freezer.TableStorageChanges, "c", nil))
	require.NoError(t, batcher.flushAll())
	require.NoError(t, batcher.sync())
	batcher.Close()
	require.NoError(t, fz.Close())

	target := memdb.NewTestDB(t)
	err = ApplyChangesetsToMDBX(context.Background(), target, dir, 0, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode acctcs")
}

// TestExecutorReadHeader_OutOfRange covers readHeader's error return when
// the freezer has no entry for the requested block.
func TestExecutorReadHeader_OutOfRange(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 1)
	defer fz.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(fz, db, chainCfg, engine, ExecutorConfig{}, nil)

	_, err := executor.readHeader(5)
	require.Error(t, err)
}

// TestCodesFreezerReader_FileSizeError covers the fileSize stat-error
// branch: an address index entry points at a cdat file that was never
// written.
func TestCodesFreezerReader_FileSizeError(t *testing.T) {
	dir := t.TempDir()
	addr := types.HexToAddress("0x000000000000000000000000000000000000ab")
	buildCodesCidx(t, dir, []types.Address{addr}, [][]byte{{0x01}})
	// Remove the cdat file out from under the already-built cidx so
	// fileSize's os.Stat fails on the end-offset lookup.
	require.NoError(t, os.Remove(dir+"/codes.0000.cdat"))

	r, err := NewCodesFreezerReader(dir)
	require.NoError(t, err)
	defer r.Close()

	_, err = r.LookupByAddress(addr)
	require.Error(t, err)
}
