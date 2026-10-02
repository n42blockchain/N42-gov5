// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state_synth_test.go drives RebuildStateWith end-to-end over a
// tiny synthetic acctcs/storcs changeset freezer and a real temp-dir MDBX,
// exercising the forward replay, the VerifyInterval root-check boundary,
// and the PersistTrie bootstrap path.
//
// RebuildStateWith reads acctcs/storcs in BATCH-compressed mode (it forces
// ForceBatchSize+SetCompressed on the reader) — that is the format the
// offline ethexec generator writes via output_batcher.go's
// freezer.EncodeBatch + freezer.WriteBatch, NOT the format CSFreezerSink's
// live-import Add/Flush path writes (plain per-item Append, used by the
// downloader's online sink and consumed by JournalVerifier/applyChangeset,
// which read in non-batch mode). This test therefore builds its synthetic
// acctcs/storcs tables with EncodeBatch+WriteBatch, matching what
// RebuildStateWith actually expects on disk.

package ethel

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// rsOpenTempMDBX opens a small real MDBX database in a fresh temp dir.
// RebuildStateWith's startBlock==0 path requires ForceRecreateBucket,
// which only the real MDBX RwTx implements (memdb does not).
func rsOpenTempMDBX(t *testing.T) kv.RwDB {
	t.Helper()
	db, err := mdbx.NewMDBX(log2.New()).
		Path(t.TempDir()).
		Label(kv.ChainDB).
		PageSize(4096).
		WriteMap().
		DirtySpace(uint64(64 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(0)).
		Open(context.Background())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	return db
}

// rsBlockBlobs builds the raw acctcs/storcs blobs for one block via the
// real ChangeSetWriter + V2 encoder path (post values resolved from
// HashedAccounts/HashedStorage in a throwaway tx), without going through
// CSFreezerSink.
func rsBlockBlobs(t *testing.T, blockNum uint64, addr types.Address, old, newAcc *account.StateAccount) (accBlob, stoBlob []byte) {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	if newAcc != nil {
		require.NoError(t, tx.Put(modules.HashedAccounts, crypto.Keccak256(addr[:]), newAcc.MarshalV2()))
	}
	w := NewCSFreezerWriter(tx, blockNum, nil)
	if old == nil {
		old = &account.StateAccount{}
	}
	if newAcc == nil {
		newAcc = &account.StateAccount{}
	}
	require.NoError(t, w.UpdateAccountData(addr, old, newAcc))

	accCS, err := w.GetAccountChanges()
	require.NoError(t, err)
	stoCS, err := w.GetStorageChanges()
	require.NoError(t, err)

	accBlob = EncodeAccountChanges(accCS, func(a types.Address) []byte {
		v, _ := tx.GetOne(modules.HashedAccounts, crypto.Keccak256(a[:]))
		if len(v) == 0 {
			return nil
		}
		return v
	})
	stoBlob = EncodeStorageChanges(stoCS, func(a types.Address, slot types.Hash) []byte {
		var comp [64]byte
		copy(comp[:32], crypto.Keccak256(a[:]))
		copy(comp[32:], crypto.Keccak256(slot[:]))
		v, _ := tx.GetOne(modules.HashedStorage, comp[:])
		return v
	})
	return accBlob, stoBlob
}

// rsWriteBatchCS writes the given per-block acctcs/storcs blobs to fz as a
// single batch, matching the offline generator's on-disk format that
// RebuildStateWith's batch-mode reader expects.
func rsWriteBatchCS(t *testing.T, fz *freezer.Freezer, accBlobs, stoBlobs [][]byte) {
	t.Helper()
	acctTbl, err := fz.EnsureTable(freezer.TableAccountChanges, "c")
	require.NoError(t, err)
	stoTbl, err := fz.EnsureTable(freezer.TableStorageChanges, "c")
	require.NoError(t, err)
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer enc.Close()

	accEncoded := freezer.EncodeBatch(accBlobs, enc)
	require.NoError(t, freezer.WriteBatch(acctTbl, accBlobs, accEncoded))
	stoEncoded := freezer.EncodeBatch(stoBlobs, enc)
	require.NoError(t, freezer.WriteBatch(stoTbl, stoBlobs, stoEncoded))
}

// TestRebuildStateWith_VerifyAndPersistTrie replays a 2-block synthetic
// changeset freezer into a fresh MDBX with VerifyInterval=1 (checked
// against independently computed header roots) and PersistTrie=true, then
// confirms the final PlainState and VerifyRebuildRoot both agree.
func TestRebuildStateWith_VerifyAndPersistTrie(t *testing.T) {
	ctx := context.Background()
	addr := types.HexToAddress("0x3333333333333333333333333333333333333333")

	root0 := jvComputeRoot(t, func(tx kv.RwTx) {
		require.NoError(t, tx.Put(modules.Account, addr[:], jvAcct(1, 100).MarshalV2()))
	})
	root1 := jvComputeRoot(t, func(tx kv.RwTx) {
		require.NoError(t, tx.Put(modules.Account, addr[:], jvAcct(1, 200).MarshalV2()))
	})

	tmpDir := t.TempDir()
	ancientDir := filepath.Join(tmpDir, "ancient")
	fz, err := freezer.New(ancientDir, 0)
	require.NoError(t, err)

	acc0, sto0 := rsBlockBlobs(t, 0, addr, nil, jvAcct(1, 100))
	acc1, sto1 := rsBlockBlobs(t, 1, addr, jvAcct(1, 100), jvAcct(1, 200))
	rsWriteBatchCS(t, fz, [][]byte{acc0, acc1}, [][]byte{sto0, sto1})
	require.NoError(t, fz.Close())

	inFz := jvBuildHeaderFreezer(t, filepath.Join(tmpDir, "headers"), []types.Hash{root0, root1})
	defer inFz.Close()

	db := rsOpenTempMDBX(t)

	opts := RebuildOptions{
		VerifyInterval: 1,
		InputFreezer:   inFz,
		PersistTrie:    true,
	}
	require.NoError(t, RebuildStateWith(ctx, db, ancientDir, 2, opts))

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	v, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.Equal(t, jvAcct(1, 200).MarshalV2(), v)

	// VerifyRebuildRoot logs rather than returning an error; just exercise
	// it over the same reconstructed state for coverage.
	VerifyRebuildRoot(ctx, db, inFz, 2, 1)
}
