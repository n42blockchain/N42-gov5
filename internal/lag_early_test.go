// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// lagPipePerFrameForTest, when positive, is the pipeline harness's
// transactions per frame (default 100), in lagPipeFramesForTest frames
// (default 4).
var lagPipePerFrameForTest, lagPipeFramesForTest int

// lagPipeResultSinkForTest, when set, receives every block's pipelined
// executed result once the harness compared it with the serial chain's.
var lagPipeResultSinkForTest func(n uint64, r rawdb.ExecutedResult)

// TestLagEarlyLevers (lag_early.go): the pipeline harness with each of the
// two levers off and on. Every sender sends in every block, so every key a
// child's transfers read from its senders is one its parent wrote, and the
// recipients rotate (a child credits an account its parent did not write,
// read from the base or its prefill). Each block's executed result -- state
// root, receipt root, bloom, gas -- must equal the serial chain's (the
// harness checks) and be the same under every switch setting; and the
// levers must have run: receipts assembled beside the fold (a), prefills
// started beside their preparations and read by the runs (b).
func TestLagEarlyLevers(t *testing.T) {
	lagPipeAwaitPrepForTest = true
	defer func() { lagPipeAwaitPrepForTest = false }()
	prevEth, prevBlake := block.UseEthereumTxRoot, block.TxRootBlake3Time
	defer func() { block.UseEthereumTxRoot, block.TxRootBlake3Time = prevEth, prevBlake }()
	defer SetAheadPrepForTest(true)()
	defer SetAheadBasePrefetchForTest(true)()
	defer SetAheadBaseClaimWaitForTest(true)()
	lagPipeRotateToForTest = true
	defer func() { lagPipeRotateToForTest = false }()

	type size struct{ frames, perFrame int }
	sizes := []size{{4, 100}}
	if !testing.Short() {
		// 17 x 500 transfers: the receipts take the parallel assembly
		// (collectParallelMinTxs) beside the fold.
		sizes = append(sizes, size{17, 500})
	}
	for _, sz := range sizes {
		var ref map[uint64]rawdb.ExecutedResult
		for _, tc := range []struct {
			name         string
			early, prep  bool
			wantOverlaps bool
		}{
			{"off", false, false, false},
			{"earlyPublish", true, false, true},
			{"prepEarly", false, true, false},
			{"both", true, true, true},
		} {
			t.Run(fmt.Sprintf("%s/%dx%d", tc.name, sz.frames, sz.perFrame), func(t *testing.T) {
				defer SetLagEarlyPublishForTest(tc.early)()
				defer SetAheadPrepEarlyForTest(tc.prep)()
				prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
				lagPipeFramesForTest, lagPipePerFrameForTest = sz.frames, sz.perFrame
				defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
				got := make(map[uint64]rawdb.ExecutedResult)
				lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { got[n] = r }
				defer func() { lagPipeResultSinkForTest = nil }()

				o0, e0 := lagRcptOverlaps.Load(), aheadPrepEarlyStarts.Load()
				u0, _, h0 := AheadBasePrefillStats()
				testLagPipelineAhead(t, false, arrivalBeforeSnapshot)
				overlaps, earlyStarts := lagRcptOverlaps.Load()-o0, aheadPrepEarlyStarts.Load()-e0
				u1, _, h1 := AheadBasePrefillStats()
				t.Logf("receipts beside the fold %d, prefills beside their preparation %d, runs on a prefill %d, lookups answered %d",
					overlaps, earlyStarts, u1-u0, h1-h0)

				if tc.wantOverlaps {
					require.GreaterOrEqual(t, overlaps, uint64(8), "the receipts were not assembled beside the fold")
				} else {
					require.Zero(t, overlaps)
				}
				if tc.prep {
					require.GreaterOrEqual(t, earlyStarts, uint64(8), "the prefills did not start beside their preparations")
				} else {
					require.Zero(t, earlyStarts)
				}
				require.GreaterOrEqual(t, u1-u0, uint64(8), "the ahead runs did not read through their prefills")
				require.NotZero(t, h1-h0)

				require.NotEmpty(t, got)
				if ref == nil {
					ref = got
					return
				}
				require.Equal(t, len(ref), len(got))
				for n, r := range ref {
					require.Equal(t, r, got[n], "block %d: result differs with the levers %s", n, tc.name)
				}
			})
		}
	}
}

// TestAheadPrepCapacity: the third queued block gets a preparation only
// with N42_AHEAD_PREP_EARLY=1.
func TestAheadPrepCapacity(t *testing.T) {
	defer SetAheadPrepEarlyForTest(false)()
	require.Equal(t, aheadPrepCap, aheadPrepCapacity())
	SetAheadPrepEarlyForTest(true)
	require.Equal(t, aheadPrepCapEarly, aheadPrepCapacity())
	require.True(t, parseOptIn("1"))
	require.True(t, parseOptIn("on"))
	require.False(t, parseOptIn(""))
	require.False(t, parseOptIn("0"))
}
