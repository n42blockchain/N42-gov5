// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// analyze_test.go drives AnalyzeChangesets (and its analyzeAccountCS /
// analyzeStorageCS helpers) against the same synthetic AccountChangeSet /
// StorageChangeSet DupSort fixtures used by account_cs_run_test.go and
// storage_cs_run_test.go.

package cscompact

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnalyzeChangesets(t *testing.T) {
	db := csOpenChangesetDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	csPutAccountChange(t, tx, 0, csAddr(0x11), []byte{1, 2, 3})
	csPutAccountChange(t, tx, 0, csAddr(0x22), nil) // zero/deleted value
	csPutAccountChange(t, tx, 1, csAddr(0x11), []byte{9})

	var slot [32]byte
	slot[31] = 1
	csPutStorageChange(t, tx, 0, csAddr(0x33), 1, slot, []byte{0xAA})
	csPutStorageChange(t, tx, 0, csAddr(0x33), 1, slot, nil)
	require.NoError(t, tx.Commit())

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	result, err := AnalyzeChangesets(roTx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, result.AccTotalEntries)
	require.EqualValues(t, 1, result.AccZeroValues)
	require.Positive(t, result.AccAvgKeyLen)
	require.NotEmpty(t, result.TopAccAddrs)

	require.EqualValues(t, 2, result.StoTotalEntries)
	require.EqualValues(t, 1, result.StoZeroValues)
	require.NotEmpty(t, result.TopStoAddrs)
}

// TestAnalyzeChangesets_SampleBlocksLimit exercises the sampleBlocks cutoff
// (only the first N distinct blocks are scanned).
func TestAnalyzeChangesets_SampleBlocksLimit(t *testing.T) {
	db := csOpenChangesetDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	for b := uint64(0); b < 5; b++ {
		csPutAccountChange(t, tx, b, csAddr(byte(b+1)), []byte{byte(b)})
	}
	require.NoError(t, tx.Commit())

	roTx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer roTx.Rollback()

	result, err := AnalyzeChangesets(roTx, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, result.AccTotalEntries)
}
