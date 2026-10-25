// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// lagPipePerFrameForTest, when positive, is the pipeline harness's
// transactions per frame (default 100), in lagPipeFramesForTest frames
// (default 4).
var lagPipePerFrameForTest, lagPipeFramesForTest int

// lagPipeResultSinkForTest, when set, receives every block's pipelined
// executed result once the harness compared it with the serial chain's.
var lagPipeResultSinkForTest func(n uint64, r rawdb.ExecutedResult)

// TestLagFinalizeLevers: the pipeline harness with N42_LAG_EARLY_PUBLISH,
// N42_FINALIZE_FAST and N42_SYSCALL_FINALIZE_ONCE off and on. Every
// block's executed result -- state root, receipt root, bloom, gas -- must
// equal the serial chain's (the harness checks) and be the same under
// every setting, and each lever must
// have run when on (receipts assembled beside the fold; the parallel root
// walk, its threshold lowered so every block takes it).
func TestLagFinalizeLevers(t *testing.T) {
	defer state.SetFinalizeFastMinForTest(64)()
	defer state.SetSyscallFinalizeParMinForTest(64)()
	lagPipeRotateToForTest = true
	defer func() { lagPipeRotateToForTest = false }()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

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
			name              string
			early, fast, once bool
		}{
			{"off", false, false, false},
			{"earlyPublish", true, false, false},
			{"finalizeFast", false, true, false},
			{"both", true, true, false},
			{"syscallOnce", false, false, true},
			{"all", true, true, true},
		} {
			t.Run(fmt.Sprintf("%s/%dx%d", tc.name, sz.frames, sz.perFrame), func(t *testing.T) {
				defer SetLagEarlyPublishForTest(tc.early)()
				defer state.SetFinalizeFastForTest(tc.fast)()
				defer state.SetSyscallFinalizeOnceForTest(tc.once)()
				prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
				lagPipeFramesForTest, lagPipePerFrameForTest = sz.frames, sz.perFrame
				defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
				got := make(map[uint64]rawdb.ExecutedResult)
				lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { got[n] = r }
				defer func() { lagPipeResultSinkForTest = nil }()

				o0, f0 := lagRcptOverlaps.Load(), state.FinalizeFastWalks()
				testLagPipelineAhead(t, false, arrivalBeforeSnapshot)
				overlaps, walks := lagRcptOverlaps.Load()-o0, state.FinalizeFastWalks()-f0
				t.Logf("receipts beside the fold %d, parallel root walks %d", overlaps, walks)
				if tc.early {
					require.GreaterOrEqual(t, overlaps, uint64(8), "the receipts were not assembled beside the fold")
				} else {
					require.Zero(t, overlaps)
				}
				if tc.fast {
					require.GreaterOrEqual(t, walks, uint64(8), "the root walks did not take the parallel loop")
				} else {
					require.Zero(t, walks)
				}

				require.NotEmpty(t, got)
				if ref == nil {
					ref = got
					return
				}
				require.Equal(t, len(ref), len(got))
				for n, r := range ref {
					require.Equal(t, r, got[n], "block %d: result differs with %s", n, tc.name)
				}
			})
		}
	}
}
