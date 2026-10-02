// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// account_cs_run_test.go drives AccountCSCompactor.readSegment and Run
// against a small in-memory MDBX "AccountChangeSet" DupSort table built
// in the Erigon changeset layout (key=blockNum BE8, dup value=addr(20)+
// oldValue), without requiring a real Erigon chaindata directory.

package cscompact

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// csOpenChangesetDB opens a small in-memory MDBX with DupSort
// AccountChangeSet + StorageChangeSet tables, mirroring the Erigon v2
// on-disk layout the compactors read from.
func csOpenChangesetDB(t *testing.T) kv.RwDB {
	t.Helper()
	db := mdbx.NewMDBX(log2.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			ErigonAccountChangeSet: {Flags: kv.DupSort},
			ErigonStorageChangeSet: {Flags: kv.DupSort},
		}
	}).MustOpen()
	t.Cleanup(db.Close)
	return db
}

func csBlockKey(block uint64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], block)
	return k[:]
}

// csPutAccountChange writes one AccountChangeSet dup entry: addr(20) +
// oldValue.
func csPutAccountChange(t *testing.T, tx kv.RwTx, block uint64, addr [20]byte, oldValue []byte) {
	t.Helper()
	v := append(append([]byte{}, addr[:]...), oldValue...)
	require.NoError(t, tx.Put(ErigonAccountChangeSet, csBlockKey(block), v))
}

func csAddr(b byte) (a [20]byte) {
	for i := range a {
		a[i] = b
	}
	return a
}

// TestAccountCSCompactor_ReadSegmentAndRun seeds a handful of blocks into a
// synthetic AccountChangeSet table, runs readSegment directly, then runs
// the full Run pipeline (segment write + idx/dat files) and reads the
// result back via OpenAccountCS/ReadBlock.
func TestAccountCSCompactor_ReadSegmentAndRun(t *testing.T) {
	db := csOpenChangesetDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutAccountChange(t, tx, 0, csAddr(0x11), []byte{1, 2, 3})
	csPutAccountChange(t, tx, 0, csAddr(0x22), nil)
	csPutAccountChange(t, tx, 1, csAddr(0x11), []byte{9})
	require.NoError(t, tx.Commit())

	compactor := NewAccountCSCompactor(db, t.TempDir())
	require.Equal(t, ErigonAccountChangeSet, compactor.tableName)
	compactor.SetTableName(ErigonAccountChangeSet) // no-op, exercises the setter

	entries, perBlock, totalBytes, err := compactor.readSegment(0, 2)
	require.NoError(t, err)
	require.Len(t, perBlock, 2)
	require.EqualValues(t, 2, perBlock[0])
	require.EqualValues(t, 1, perBlock[1])
	require.Len(t, entries, 3)
	require.Positive(t, totalBytes)

	outDir := compactor.outputDir
	require.NoError(t, compactor.Run(ctx, 0, 2))

	reader, err := OpenAccountCS(outDir)
	require.NoError(t, err)
	defer reader.Close()

	got0, err := reader.ReadBlock(0)
	require.NoError(t, err)
	require.Len(t, got0, 2)
	got1, err := reader.ReadBlock(1)
	require.NoError(t, err)
	require.Len(t, got1, 1)

	// A second Run over a wider range against the same output dir exercises
	// the resume path (existing idx entries -> skip already-written
	// segments) and the "reopen last dat file" / headFile bookkeeping.
	tx2, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutAccountChange(t, tx2, 2, csAddr(0x33), []byte{4})
	require.NoError(t, tx2.Commit())

	compactor2 := NewAccountCSCompactor(db, outDir)
	require.NoError(t, compactor2.Run(ctx, 0, 3))
}
