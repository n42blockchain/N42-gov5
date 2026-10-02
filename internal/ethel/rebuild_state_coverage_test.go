// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state_coverage_test.go covers RebuildState's thin wrapper,
// flushToMDBX's write/wipe paths, deleteBatch, and rebuildEVMFallback
// against a tiny synthetic geth freezer — the pieces
// rebuild_state_synth_test.go's end-to-end RebuildStateWith test doesn't
// reach directly.

package ethel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules"
)

// TestRebuildState_EmptyTableErrors confirms the exported RebuildState
// wrapper forwards to RebuildStateWith and surfaces the same "acctcs table
// is empty" error on a freshly created, unpopulated ancient dir.
func TestRebuildState_EmptyTableErrors(t *testing.T) {
	ctx := context.Background()
	db := rsOpenTempMDBX(t)
	ancientDir := t.TempDir()

	// NewFreezerTableReadOnly on a directory with no existing table files
	// still succeeds (0 items); RebuildStateWith then rejects the empty
	// acctcs table before touching MDBX.
	err := RebuildState(ctx, db, ancientDir, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "acctcs")
}

// TestFlushToMDBX_EmptyIsNoOp exercises the early-return branch when all
// three maps are empty.
func TestFlushToMDBX_EmptyIsNoOp(t *testing.T) {
	db := rsOpenTempMDBX(t)
	err := flushToMDBX(context.Background(), db, nil, nil, nil)
	require.NoError(t, err)
}

// TestFlushToMDBX_WritesThenWipes writes an account + one storage slot,
// confirms both land in MDBX, then wipes the address and confirms the
// storage row is prefix-deleted while an unrelated address with no wipe
// survives.
func TestFlushToMDBX_WritesThenWipes(t *testing.T) {
	ctx := context.Background()
	db := rsOpenTempMDBX(t)

	addrWiped := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addrKept := types.HexToAddress("0x2222222222222222222222222222222222222222")
	slot := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000001")

	acctMap := map[types.Address][]byte{
		addrWiped: {0xAA},
		addrKept:  {0xBB},
	}
	storMap := map[types.Address]map[types.Hash][]byte{
		addrWiped: {slot: {0x01}},
		addrKept:  {slot: {0x02}},
	}
	require.NoError(t, flushToMDBX(ctx, db, acctMap, storMap, nil))

	func() {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		defer tx.Rollback()
		v, err := tx.GetOne(modules.Account, addrWiped[:])
		require.NoError(t, err)
		require.Equal(t, []byte{0xAA}, v)
	}()

	// Now wipe addrWiped's storage: wipeSet alone, no new storage/account
	// entries for it this round.
	wipeSet := map[types.Address]struct{}{addrWiped: {}}
	require.NoError(t, flushToMDBX(ctx, db, nil, nil, wipeSet))

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	wipedKey := make([]byte, 52)
	copy(wipedKey[:20], addrWiped[:])
	copy(wipedKey[20:], slot[:])
	v, err := tx.GetOne(modules.Storage, wipedKey)
	require.NoError(t, err)
	require.Empty(t, v, "wiped address's storage slot must be gone")

	keptKey := make([]byte, 52)
	copy(keptKey[:20], addrKept[:])
	copy(keptKey[20:], slot[:])
	v2, err := tx.GetOne(modules.Storage, keptKey)
	require.NoError(t, err)
	require.Equal(t, []byte{0x02}, v2, "unrelated address's storage must survive the wipe")

	// Account delete path: nil value means "delete".
	acctMap2 := map[types.Address][]byte{addrKept: nil}
	tx.Rollback()
	require.NoError(t, flushToMDBX(ctx, db, acctMap2, nil, nil))
	tx2, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx2.Rollback()
	v3, err := tx2.GetOne(modules.Account, addrKept[:])
	require.NoError(t, err)
	require.Empty(t, v3)
}

// TestDeleteBatch_DrainsInLimitedChunks writes a handful of account rows and
// confirms deleteBatch removes at most `limit` per call, needing multiple
// calls to fully drain, finally returning 0 once empty.
func TestDeleteBatch_DrainsInLimitedChunks(t *testing.T) {
	ctx := context.Background()
	db := rsOpenTempMDBX(t)

	const n = 5
	func() {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		defer tx.Rollback()
		for i := 0; i < n; i++ {
			var addr types.Address
			addr[19] = byte(i + 1)
			require.NoError(t, tx.Put(modules.Account, addr[:], []byte{byte(i)}))
		}
		require.NoError(t, tx.Commit())
	}()

	deleted, err := deleteBatch(ctx, db, modules.Account, 2)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	deleted, err = deleteBatch(ctx, db, modules.Account, 2)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	deleted, err = deleteBatch(ctx, db, modules.Account, 2)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	deleted, err = deleteBatch(ctx, db, modules.Account, 2)
	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

// TestRebuildEVMFallback_EmptyBlock drives rebuildEVMFallback over block 1 of
// a 2-block synthetic empty-body geth freezer. rebuildEVMFallback writes its
// "correct changeset" patch files to the current working directory
// unconditionally once ChangeSetWriter() is non-nil, so the test chdirs into
// a scratch temp dir for the call and restores cwd afterward.
func TestRebuildEVMFallback_EmptyBlock(t *testing.T) {
	ctx := context.Background()
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 2)
	defer fz.Close()

	db := rsOpenTempMDBX(t)
	chainCfg := ethWReplayEthConfig()

	origWD, err := os.Getwd()
	require.NoError(t, err)
	scratch := t.TempDir()
	require.NoError(t, os.Chdir(scratch))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	opts := RebuildOptions{
		ChainConfig: chainCfg,
		GethFreezer: fz,
	}
	require.NoError(t, rebuildEVMFallback(ctx, db, opts, 1))

	// Patch files land in the scratch cwd (empty block: zero changes, but
	// the encoder still runs and writes zero-length-payload files).
	_, statErr := os.Stat(filepath.Join(scratch, "storcs_patch_1.bin"))
	require.NoError(t, statErr)
	_, statErr = os.Stat(filepath.Join(scratch, "acctcs_patch_1.bin"))
	require.NoError(t, statErr)
}
