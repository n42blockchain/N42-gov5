// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestLagEarlySnapshot runs the pipeline harness (every block's result
// equals the serial chain's, imports in parent order, an injected write
// failure at block 5 is retried) with N42_LAG_EARLY_SNAPSHOT=1 at deferred
// depth 3, so a block's child is usually queued before the block's
// Block-STM run ends: the child then starts on the block's early snapshot
// and is taken by its import. Recipients rotate so a child reads accounts
// its parent wrote (answered by the parent's MVS) and accounts written two
// blocks below (answered by the layers under it).
func TestLagEarlySnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ordered bool
		// unsafe: every block credits zero to its first sender after its
		// fold -- a write outside the hold set that leaves the state as
		// it was. The first child that started early on such a block is
		// discarded, the sender joins the hold set, and later children's
		// reads of it wait for the complete post-state.
		unsafe bool
	}{{"inline", false, false}, {"orderedWriter", true, false}, {"writeAfterFold", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			if tc.ordered {
				defer SetLagWriteAsyncForTest(true)()
			}
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
			// Each ahead run starts once its own child is queued (or 2 s
			// passed: the last block has none), so the child is there when
			// the run's Block-STM ends.
			lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if bc.lagQueuedChild(b.Hash()) != nil {
						return
					}
				}
			}
			defer func() { lagPipeAheadClaimedForTest = nil }()
			var sender types.Address
			if tc.unsafe {
				// The harness's first sender (frametest.Keys(t, 65, 83)[1:]).
				sender = crypto.PubkeyToAddress(frametest.Keys(t, 65, 83)[1].PublicKey)
				lagEarlyAfterFoldForTest = func(ibs *state.IntraBlockState, _ []*transaction.Transaction) {
					ibs.AddBalance(sender, new(uint256.Int))
				}
				defer func() { lagEarlyAfterFoldForTest = nil }()
			}
			var held []types.Address
			var holdWaits uint64
			// Results are compared block by block with the serial chain by
			// the harness; count the blocks.
			var results int
			lagPipeResultSinkForTest = func(uint64, rawdb.ExecutedResult) { results++ }
			defer func() { lagPipeResultSinkForTest = nil }()

			var stats func() (pub, launched, used, unsafe, dropped uint64)
			lagPipeEngineDoneForTest = func(lc *lagChain) {
				s := &lc.bc.lagPipe.earlyStats
				stats = func() (uint64, uint64, uint64, uint64, uint64) {
					return s.published.Load(), s.launched.Load(), s.used.Load(), s.unsafe.Load(), s.dropped.Load()
				}
				lc.bc.lagPipe.mu.Lock()
				defer lc.bc.lagPipe.mu.Unlock()
				require.Nil(t, lc.bc.lagPipe.next, "a next run outlived its blocks")
				held = append(held, lc.bc.lagPipe.earlyHold...)
				holdWaits = earlyHoldWaits.Load()
				t.Logf("early hold learned: %v, held reads %d", held, holdWaits)
			}
			defer func() { lagPipeEngineDoneForTest = nil }()

			testLagPipelineAheadEngine(t, false, arrivalAny, tc.ordered)
			require.Equal(t, 12, results)
			pub, launched, used, unsafe, dropped := stats()
			t.Logf("early snapshots: published %d, next runs launched %d, used %d, unsafe %d, dropped %d", pub, launched, used, unsafe, dropped)
			require.NotZero(t, pub, "no early snapshot was published")
			require.NotZero(t, launched, "no child started on an early snapshot")
			require.GreaterOrEqual(t, used, uint64(6), "children started early were not used")
			if !tc.unsafe {
				require.Zero(t, unsafe, "a block wrote after its fold outside the hold set")
				return
			}
			require.NotZero(t, unsafe, "the write after the fold was not detected")
			require.Contains(t, held, sender, "the written address did not join the hold set")
			require.NotZero(t, holdWaits, "no read waited for a complete post-state")
		})
	}
}

// TestLagEarlySnapshotReleasesStore: an early snapshot's executor is
// released exactly once, after its publication and the end of every run
// reading it, and a dropped snapshot fails the reads it held back.
func TestLagEarlySnapshotReleasesStore(t *testing.T) {
	freed := 0
	es := &earlySnap{done: make(chan struct{}), free: func() { freed++ }}
	es.refs.Store(2) // the publisher and one reading run
	es.end(nil, false)
	require.Zero(t, freed, "released while a run still reads it")
	_, err := es.WaitFull()
	require.ErrorIs(t, err, errEarlyDropped)
	es.end(nil, false) // once
	es.unref()
	require.Equal(t, 1, freed)

	held := &earlySnap{done: make(chan struct{}), hold: nil}
	go func() {
		time.Sleep(5 * time.Millisecond)
		held.end(nil, false)
	}()
	_, err = held.WaitFull()
	require.ErrorIs(t, err, errEarlyDropped)
}
