// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/modules/state"
)

// TestLagWriterQFlushReadySameRows (N42_LAG_WRITE_QFLUSH_READY with
// N42_LAG_WRITE_TXN_EARLY): block by block, the early transaction that
// writes the QMDB rows before or after the state rows -- forced either way,
// and as their inputs come ready -- stores exactly the rows of the default
// order, in every table.
func TestLagWriterQFlushReadySameRows(t *testing.T) {
	for _, mode := range []string{"qflushFirst", "stateFirst", "alternate", "ready"} {
		t.Run(mode, func(t *testing.T) {
			defer block.SetDeferredDepthForTest(2, 0)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer state.SetHistoryIndexDeferredForTest(true)()
			keys := frametest.Keys(t, 3, 76)
			const n = 8

			restore := SetLagWriteTxnEarlyForTest(false)
			ref := newLagChainEngine(t, keys, importBenchEngine{})
			blks, want := preparedWriteBlocks(t, ref, n)
			restore()

			switch mode {
			case "qflushFirst":
				lagQFlushFirstForTest = func(uint64) bool { return true }
			case "stateFirst":
				lagQFlushFirstForTest = func(uint64) bool { return false }
			case "alternate":
				lagQFlushFirstForTest = func(n uint64) bool { return n%2 == 0 }
			}
			defer func() { lagQFlushFirstForTest = nil }()
			var mu sync.Mutex
			early := 0
			blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
				mu.Lock()
				if ph.TxnEarly {
					early++
				}
				mu.Unlock()
			}
			defer func() { blockWritePhasesHook = nil }()
			defer SetLagWriteTxnEarlyForTest(true)()
			defer SetLagWriteQFlushReadyForTest(true)()
			lagWriteBeforeCommitForTest = func(uint64) error {
				time.Sleep(5 * time.Millisecond)
				return nil
			}
			defer func() { lagWriteBeforeCommitForTest = nil }()

			seq := newLagChainEngine(t, keys, importBenchEngine{})
			for i, b := range blks {
				seq.bc.RememberAvailable(b)
				seq.bc.EnqueueLagImport(b)
				waitLagWritten(t, seq.bc, uint64(i+1))
				seq.bc.lock.Lock()
				require.NoError(t, seq.bc.joinLagWrite())
				seq.bc.lock.Unlock()
				requireSameTables(t, want[i], dumpTables(t, seq.db), "after block "+itoa(i+1))
			}
			mu.Lock()
			require.Equal(t, n, early, "writes that took the early transaction")
			mu.Unlock()
		})
	}
}

// TestLagQFlushFirst: the order rule -- the QMDB rows first only while the
// preparation runs and the capture is done; without a preparation, or
// with it done, the state first.
func TestLagQFlushFirst(t *testing.T) {
	prepDone := &stateWritePrep{done: make(chan struct{})}
	close(prepDone.done)
	prepBusy := &stateWritePrep{done: make(chan struct{})}
	require.False(t, lagQFlushFirst(&lagWriteJob{}), "no preparation")
	require.False(t, lagQFlushFirst(&lagWriteJob{prep: prepDone}), "preparation done")
	require.True(t, lagQFlushFirst(&lagWriteJob{prep: prepBusy}), "preparation running, rows captured at the hand-off")
	require.True(t, chanClosed(prepDone.done))
	require.False(t, chanClosed(make(chan struct{})))
}
