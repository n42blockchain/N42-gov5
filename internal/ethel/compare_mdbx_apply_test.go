// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// compare_mdbx_apply_test.go covers ApplyChangesetsToMDBX. Account/storage
// changeset blobs are written through the same outputBatcher-driven,
// batch-compressed acctcs/storcs format the production async output
// writer uses (ApplyChangesetsToMDBX's reader is configured with
// ForceBatchSize+SetCompressed, so a plain CSFreezerSink.Flush-style
// single-item append would silently decode as empty).

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
)

func TestApplyChangesetsToMDBX(t *testing.T) {
	dir := t.TempDir()

	srcDB := memdb.NewTestDB(t)
	srcTx, err := srcDB.BeginRw(context.Background())
	require.NoError(t, err)
	defer srcTx.Rollback()

	fz, err := freezer.New(dir, freezer.DefaultFreezeThreshold)
	require.NoError(t, err)
	batcher, err := newOutputBatcher(fz)
	require.NoError(t, err)

	addrA := types.HexToAddress("0x4444444444444444444444444444444444444")
	addrB := types.HexToAddress("0x5555555555555555555555555555555555555")
	slot := types.HexToHash("0x09")
	codeHash := types.BytesToHash(crypto.Keccak256(nil))

	resolveAcc := func(addr types.Address) []byte {
		v, _ := srcTx.GetOne(modules.HashedAccounts, crypto.Keccak256(addr[:]))
		if len(v) == 0 {
			return nil
		}
		return v
	}
	resolveSto := func(addr types.Address, s types.Hash) []byte {
		var comp [64]byte
		copy(comp[:32], crypto.Keccak256(addr[:]))
		copy(comp[32:], crypto.Keccak256(s[:]))
		v, _ := srcTx.GetOne(modules.HashedStorage, comp[:])
		return v
	}
	writeBlock := func(blockNum uint64, fn func(csw *state.ChangeSetWriter)) {
		csw := state.NewChangeSetWriterPlain(srcTx, blockNum)
		fn(csw)
		accCS, err := csw.GetAccountChanges()
		require.NoError(t, err)
		stoCS, err := csw.GetStorageChanges()
		require.NoError(t, err)
		accBlob := EncodeAccountChanges(accCS, resolveAcc)
		stoBlob := EncodeStorageChanges(stoCS, resolveSto)
		require.NoError(t, batcher.addEntry(freezer.TableAccountChanges, "c", accBlob))
		require.NoError(t, batcher.addEntry(freezer.TableStorageChanges, "c", stoBlob))
	}

	// Block 0: addrA created with balance 7, one storage slot set.
	postA := &account.StateAccount{Initialised: true, Nonce: 0}
	postA.Balance.SetUint64(7)
	postA.CodeHash = codeHash
	require.NoError(t, srcTx.Put(modules.HashedAccounts, crypto.Keccak256(addrA[:]), postA.MarshalV2()))
	var comp [64]byte
	copy(comp[:32], crypto.Keccak256(addrA[:]))
	copy(comp[32:], crypto.Keccak256(slot[:]))
	require.NoError(t, srcTx.Put(modules.HashedStorage, comp[:], []byte{0x42}))
	writeBlock(0, func(csw *state.ChangeSetWriter) {
		emptyA := account.NewAccount()
		require.NoError(t, csw.UpdateAccountData(addrA, &emptyA, postA))
		var zero, val uint256.Int
		val.SetUint64(0x42)
		require.NoError(t, csw.WriteAccountStorage(addrA, slot, zero, val))
	})

	// Block 1: addrB created.
	postB := &account.StateAccount{Initialised: true, Nonce: 1}
	postB.Balance.SetUint64(1)
	postB.CodeHash = codeHash
	require.NoError(t, srcTx.Put(modules.HashedAccounts, crypto.Keccak256(addrB[:]), postB.MarshalV2()))
	writeBlock(1, func(csw *state.ChangeSetWriter) {
		emptyB := account.NewAccount()
		require.NoError(t, csw.UpdateAccountData(addrB, &emptyB, postB))
	})

	// Block 2: addrB fully removed (post-block value absent) — exercises
	// the NewValue-empty delete branch in ApplyChangesetsToMDBX.
	require.NoError(t, srcTx.Delete(modules.HashedAccounts, crypto.Keccak256(addrB[:])))
	writeBlock(2, func(csw *state.ChangeSetWriter) {
		emptyB := account.NewAccount()
		require.NoError(t, csw.UpdateAccountData(addrB, postB, &emptyB))
	})

	require.NoError(t, batcher.flushAll())
	require.NoError(t, batcher.sync())
	batcher.Close()
	require.NoError(t, fz.Close()) // ApplyChangesetsToMDBX reopens the tables from disk

	targetDB := memdb.NewTestDB(t)
	require.NoError(t, ApplyChangesetsToMDBX(context.Background(), targetDB, dir, 0, 3))

	tx, err := targetDB.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	gotA, err := tx.GetOne(modules.Account, addrA[:])
	require.NoError(t, err)
	require.Equal(t, postA.MarshalV2(), gotA)

	gotSlot, err := tx.GetOne(modules.Storage, append(append([]byte{}, addrA[:]...), slot[:]...))
	require.NoError(t, err)
	require.NotEmpty(t, gotSlot)

	gotB, err := tx.GetOne(modules.Account, addrB[:])
	require.NoError(t, err)
	require.Empty(t, gotB, "addrB's final changeset NewValue is empty -> row must be deleted")
}
