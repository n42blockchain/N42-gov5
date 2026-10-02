// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// storage_cs_run_test.go drives StorageCSCompactor.readSegment and Run
// against a small in-memory MDBX "StorageChangeSet" DupSort table built in
// the Erigon changeset layout (key=blockNum(8)+addr(20)+incarnation(8),
// value=slot(32)+oldValue), using AutoDupSortKeysConversion the same way
// the real Erigon chaindata does.

package cscompact

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// csOpenStorageChangesetDB opens a small in-memory MDBX with a DupSort
// StorageChangeSet table in the Erigon auto-dupsort-conversion layout.
func csOpenStorageChangesetDB(t *testing.T) kv.RwDB {
	t.Helper()
	db := mdbx.NewMDBX(log2.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			ErigonStorageChangeSet: {
				Flags:                     kv.DupSort,
				AutoDupSortKeysConversion: true,
				DupFromLen:                36,
				DupToLen:                  8,
			},
		}
	}).MustOpen()
	t.Cleanup(db.Close)
	return db
}

func csStorageKey(block uint64, addr [20]byte, incarnation uint64) []byte {
	k := make([]byte, 36)
	binary.BigEndian.PutUint64(k[:8], block)
	copy(k[8:28], addr[:])
	binary.BigEndian.PutUint64(k[28:36], incarnation)
	return k
}

func csPutStorageChange(t *testing.T, tx kv.RwTx, block uint64, addr [20]byte, incarnation uint64, slot [32]byte, oldValue []byte) {
	t.Helper()
	v := append(append([]byte{}, slot[:]...), oldValue...)
	require.NoError(t, tx.Put(ErigonStorageChangeSet, csStorageKey(block, addr, incarnation), v))
}

// TestStorageCSCompactor_ReadSegmentAndRun seeds a handful of blocks into a
// synthetic StorageChangeSet table, runs readSegment directly, then runs
// Run end-to-end and decodes the resulting segment file.
func TestStorageCSCompactor_ReadSegmentAndRun(t *testing.T) {
	db := csOpenStorageChangesetDB(t)
	ctx := context.Background()

	addr1 := csAddr(0x44)
	addr2 := csAddr(0x55)
	var slotA, slotB [32]byte
	slotA[31] = 1
	slotB[31] = 2

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutStorageChange(t, tx, 0, addr1, 1, slotA, []byte{0xAA})
	csPutStorageChange(t, tx, 0, addr2, 1, slotB, nil)
	csPutStorageChange(t, tx, 1, addr1, 1, slotA, []byte{0xBB, 0xCC})
	require.NoError(t, tx.Commit())

	compactor := NewStorageCSCompactor(db, t.TempDir())
	require.Equal(t, ErigonStorageChangeSet, compactor.tableName)
	compactor.SetTableName(ErigonStorageChangeSet)

	entries, perBlock, totalBytes, err := compactor.readSegment(0, 2)
	require.NoError(t, err)
	require.Len(t, perBlock, 2)
	require.EqualValues(t, 2, perBlock[0])
	require.EqualValues(t, 1, perBlock[1])
	require.Len(t, entries, 3)
	require.Positive(t, totalBytes)
	require.Equal(t, types.Address(addr1), entries[0].Address)
	require.Equal(t, types.Hash(slotA), entries[0].Slot)

	outDir := compactor.outputDir
	require.NoError(t, compactor.Run(ctx, 0, 2))

	// Decode the written segment directly off disk.
	datPath := filepath.Join(outDir, "storcs.0000.cdat")
	raw, err := os.ReadFile(datPath)
	require.NoError(t, err)
	require.Greater(t, len(raw), 4)
	size := binary.LittleEndian.Uint32(raw[:4])
	compressed := raw[4 : 4+size]

	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	defer dec.Close()
	decoded, decodedPerBlock, err := DecodeStorageCSSegment(compressed, dec)
	require.NoError(t, err)
	require.Len(t, decoded, 3)
	require.Equal(t, perBlock, decodedPerBlock)

	// Second Run over a wider range exercises the resume path.
	tx2, err := db.BeginRw(ctx)
	require.NoError(t, err)
	var slotC [32]byte
	slotC[31] = 3
	csPutStorageChange(t, tx2, 2, addr1, 1, slotC, []byte{1})
	require.NoError(t, tx2.Commit())

	compactor2 := NewStorageCSCompactor(db, outDir)
	require.NoError(t, compactor2.Run(ctx, 0, 3))
}
