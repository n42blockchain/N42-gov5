// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/modules/state"
)

// TestLagWriterPrepShardsSameRows (N42_LAG_WRITE_PREP_SHARDS): a chain whose
// ordered writer builds the prepared rows and changesets in parts stores,
// block by block, exactly the tables of a chain that builds them in one
// pass -- over every kind of state write the prepared path covers, a
// pipelined import, and a write that fails once and is executed again
// after the tree is reloaded from the store.
func TestLagWriterPrepShardsSameRows(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer state.SetHistoryIndexDeferredForTest(true)()
	keys := frametest.Keys(t, 3, 72)
	const n = 16

	restore := state.SetPrepShardsForTest(0, 0)
	ref := newLagChainEngine(t, keys, importBenchEngine{})
	blks, want := preparedWriteBlocks(t, ref, n)
	restore()

	defer state.SetPrepShardsForTest(4, 1)()
	before := state.PreparedShardedWrites()

	// Block by block.
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
	require.GreaterOrEqual(t, state.PreparedShardedWrites()-before, uint64(n-2), "writes prepared in parts")

	// Pipelined.
	pipe := newLagChainEngine(t, keys, importBenchEngine{})
	for _, b := range blks {
		pipe.bc.RememberAvailable(b)
		pipe.bc.EnqueueLagImport(b)
	}
	waitLagWritten(t, pipe.bc, n)
	pipe.bc.lock.Lock()
	require.NoError(t, pipe.bc.joinLagWrite())
	pipe.bc.lock.Unlock()
	requireSameTables(t, want[n-1], dumpTables(t, pipe.db), "pipelined import")

	// Recovery: block 6's write fails once; the tree is reloaded and the
	// block executed and prepared again.
	var failed atomic.Bool
	lagWriteBeforeCommitForTest = func(num uint64) error {
		if num == 6 && failed.CompareAndSwap(false, true) {
			return errors.New("injected write failure (test)")
		}
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	rec := newLagChainEngine(t, keys, importBenchEngine{})
	for _, b := range blks {
		rec.bc.RememberAvailable(b)
		rec.bc.EnqueueLagImport(b)
	}
	waitLagWritten(t, rec.bc, n)
	rec.bc.lock.Lock()
	require.NoError(t, rec.bc.joinLagWrite())
	rec.bc.lock.Unlock()
	require.True(t, failed.Load(), "the injected failure fired")
	_, failedWrites, _ := rec.bc.LagWriteStats()
	require.EqualValues(t, 1, failedWrites, "writes that failed")
	requireSameTables(t, want[n-1], dumpTables(t, rec.db), "after a failed write")
}
