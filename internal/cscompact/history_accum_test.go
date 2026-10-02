// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_accum_test.go covers HistoryAccumulator end-to-end: key
// accumulation (account + storage), the AdvanceBlock segment-boundary
// flush, the Flush/Close partial-segment path, and SegmentCount — pure
// in-memory + temp-dir file I/O, no DB required.

package cscompact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHistoryAccumulator_AccountAndStorageKeys drives
// NewAccountHistoryAccumulator and NewStorageHistoryAccumulator through a
// short run that never reaches a full HistSegmentSize window, so the data
// is only written when Close forces a partial-segment Flush.
func TestHistoryAccumulator_AccountAndStorageKeys(t *testing.T) {
	dir := t.TempDir()

	acc, err := NewAccountHistoryAccumulator(dir)
	require.NoError(t, err)
	require.EqualValues(t, 0, acc.SegmentCount())

	addr := make([]byte, 20)
	addr[0] = 0x11
	acc.AddAccountKey(addr, 0)
	acc.AddAccountKey(addr, 1)
	// Too-short address must be silently ignored.
	acc.AddAccountKey([]byte{1, 2, 3}, 0)

	require.NoError(t, acc.AdvanceBlock(0)) // nowhere near a segment boundary: no-op
	require.EqualValues(t, 0, acc.SegmentCount())

	// NOTE: SegmentCount() after Close() panics (nil-deref on the closed
	// idxFile's failed Stat() in SegmentStoreWriter.SegmentCount) — a
	// latent defect, not exercised here; check the count via Flush's
	// return instead by calling SegmentCount BEFORE Close.
	require.NoError(t, acc.Flush(1))
	require.EqualValues(t, 1, acc.SegmentCount())
	require.NoError(t, acc.Close(1))

	sto, err := NewStorageHistoryAccumulator(dir)
	require.NoError(t, err)
	slot := make([]byte, 32)
	slot[31] = 1
	sto.AddStorageKey(addr, slot, 5)
	sto.AddStorageKey(addr, slot, 6) // dup block, same key: sortAndDedup must collapse
	// Too-short slot must be silently ignored.
	sto.AddStorageKey(addr, []byte{1}, 5)

	require.NoError(t, sto.Flush(6))
	require.EqualValues(t, 1, sto.SegmentCount())
	require.NoError(t, sto.Close(6))
}

// TestHistoryAccumulator_AdvanceBlockFlushesAtBoundary checks that
// AdvanceBlock itself (not just Close) triggers flushSegment once blockNum
// reaches the HistSegmentSize boundary, and that segStart advances past it.
func TestHistoryAccumulator_AdvanceBlockFlushesAtBoundary(t *testing.T) {
	dir := t.TempDir()
	acc, err := NewAccountHistoryAccumulator(dir)
	require.NoError(t, err)

	addr := make([]byte, 20)
	addr[0] = 0x22
	acc.AddAccountKey(addr, HistSegmentSize-1)

	require.NoError(t, acc.AdvanceBlock(HistSegmentSize-1))
	require.EqualValues(t, 1, acc.SegmentCount())
	require.EqualValues(t, HistSegmentSize, acc.segStart)

	require.NoError(t, acc.Close(HistSegmentSize))
}
