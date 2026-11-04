// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/state"
)

// lagPipeTablesSinkForTest, when set, receives the pipeline harness's
// pipelined chain once every block is written and checked.
var lagPipeTablesSinkForTest func(piped *lagChain)

// lagPipeDistinctToForTest: the pipeline harness sends every transfer of a
// block to its own account, the same accounts in every block.
var lagPipeDistinctToForTest bool

// TestLagWriterEarlyPrepSameRows (r67a, N42_LAG_WRITE_EARLY_PREP): the
// ordered-writer pipeline harness -- every block executed ahead, every write
// handed off, one injected write failure that leaves an early preparation
// without a write -- run with the state write's preparation started at the
// hand-off (off) and inside the root (on). The early preparation must have
// run for the blocks when on and never when off, and the two chains must
// end with exactly the same rows in every table, with the history index
// deferred (prepared rows) and inline (replayed calls).
//
// The "wide" runs (not in -short) send every transfer of 17 x 500 to its
// own account (the same ones in every block) under the bench's state
// flags, so the root takes the detached credits and the parallel walk and
// the preparation the sharded, parallel-load path -- the case where the
// preparation overlaps the most of the root computer.
func TestLagWriterEarlyPrepSameRows(t *testing.T) {
	type tc struct {
		name     string
		deferred bool
		wide     bool
	}
	cases := []tc{{"historyDeferred", true, false}, {"historyInline", false, false}}
	if !testing.Short() {
		cases = append(cases, tc{"wide/historyDeferred", true, true}, tc{"wide/historyInline", false, true})
	}
	for _, c := range cases {
		deferred := c.deferred
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
				lagPipeFramesForTest, lagPipePerFrameForTest = 17, 500
				defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
				lagPipeDistinctToForTest = true
				defer func() { lagPipeDistinctToForTest = false }()
				defer state.SetFinalizeFastForTest(true)()
				defer state.SetFinalizeFastMinForTest(64)()
				defer state.SetFoldNoJournalForTest(true)()
				defer state.SetFinalizeHoistForTest(true)()
				defer state.SetPrehookCutForTest(1)()
				defer state.SetPrepLoadParForTest(8)()
				defer state.SetPrepShardsForTest(8)()
			}
			defer state.SetHistoryIndexDeferredForTest(deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			// The preparation starts in the root (a delayed one never
			// starts for the failed write; lag_r68a_test.go covers it).
			defer SetLagWriteEarlyPrepDelayForTest(false, 0)()
			// Slow writes, so every child imports (and its preparation
			// starts) while its parent's write is in flight.
			lagWriteBeforeCommitForTest = func(uint64) error {
				time.Sleep(30 * time.Millisecond)
				return nil
			}
			defer func() { lagWriteBeforeCommitForTest = nil }()

			var (
				mu         sync.Mutex
				early, all int
				sharded    int
				detached   int
				lead       time.Duration
			)
			blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
				mu.Lock()
				defer mu.Unlock()
				if ph.Prep > 0 {
					all++
				}
				if d, _ := state.LastRootCredits(); d > detached {
					detached = d
				}
				if ph.PrepSharded {
					sharded++
				}
				if ph.PrepEarly {
					early++
					lead += ph.PrepLead
				}
			}
			defer func() { blockWritePhasesHook = nil }()

			run := func(on bool) map[string]string {
				defer SetLagWriteEarlyPrepForTest(on)()
				var tables map[string]string
				lagPipeTablesSinkForTest = func(piped *lagChain) { tables = dumpTables(t, piped.db) }
				defer func() { lagPipeTablesSinkForTest = nil }()
				mu.Lock()
				early, all, lead, sharded, detached = 0, 0, 0, 0, 0
				mu.Unlock()
				e0 := lagEarlyPreps.Load()
				testLagPipelineAheadEngine(t, false, arrivalBeforeSnapshot, true)
				started := lagEarlyPreps.Load() - e0
				mu.Lock()
				defer mu.Unlock()
				t.Logf("early=%v: preparations started in the root %d, writes %d prepared, %d early (mean lead %v), %d sharded, most detached credits %d",
					on, started, all, early, lead/time.Duration(max(early, 1)), sharded, detached)
				if c.wide {
					require.GreaterOrEqual(t, detached, 2048, "the roots took no detached credits")
					if deferred {
						// Inline history replays the calls (no sharded rows).
						require.GreaterOrEqual(t, sharded, 8, "writes prepared on the sharded path")
					}
				}
				if on {
					// The failed write's preparation started early too, and
					// the block's retry prepares again.
					require.GreaterOrEqual(t, early, 8, "writes whose preparation started in the root")
					require.Greater(t, started, uint64(early), "the failed write's early preparation")
				} else {
					require.Zero(t, started)
					require.Zero(t, early)
				}
				require.NotEmpty(t, tables)
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "early preparation")
		})
	}
}
