// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestLagQCapEarlySameRows (N42_LAG_QCAP_EARLY, Q1): the ordered-writer
// pipeline harness (every block executed ahead, slow writes so each capture
// overlaps the previous write, an injected import failure) ends with the
// same rows in every table -- QMDB entries, twigs, leaves, undo, applied
// marker, executed results, changesets, receipts -- with the switch off, on
// and check, with the transfer lanes off and on, the changed-twig set and
// the lean SetCold on. The executed results also equal a serial import's
// (the harness compares them block by block).
func TestLagQCapEarlySameRows(t *testing.T) {
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetLagHandoffLeanForTest(true)()
	defer qmdb.SetFlushTwigSetForTest("1")()
	defer commitment.SetImportSetColdLeanForTest("1")()
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	for _, lanes := range []string{"off", "on"} {
		t.Run("lanes="+lanes, func(t *testing.T) {
			defer SetTransferLanesForTest(lanes)()
			run := func(mode string) map[string]string {
				defer SetLagQCapEarlyForTest(mode)()
				c0, tl0, v0 := LagQCapEarlyStats()
				tables := r68aRunTables(t)
				c1, tl1, v1 := LagQCapEarlyStats()
				t.Logf("early=%q: early captures %d, tails run by the capture %d", mode, c1-c0, tl1-tl0)
				require.Zero(t, v1-v0, "early-capture check violations")
				if mode == "" {
					require.Zero(t, c1-c0)
				} else {
					require.GreaterOrEqual(t, c1-c0, uint64(8), "hand-offs captured before the join")
				}
				return tables
			}
			want := run("")
			requireSameTables(t, want, run("1"), "early capture on")
			requireSameTables(t, want, run("check"), "early capture check")
		})
	}
}

// TestLagQCapEarlyFailedPrevWrite (Q5): in the ordered-writer pipeline
// harness (ahead runs, so each hand-off finds the previous write in flight)
// write N-1 fails while block N's early capture exists: the failing write
// waits until N's hand-off has started its capture and joins it. The join
// aborts both windows, reloads the tree and re-queues N-1; the harness
// checks every block's result against a serial import, and the store ends
// with the same rows in every table as the same failure with the switch
// off.
func TestLagQCapEarlyFailedPrevWrite(t *testing.T) {
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetLagHandoffLeanForTest(true)()
	const failAt = 7
	lagPipeWriteFailsForTest = 1
	defer func() { lagPipeWriteFailsForTest = 0 }()
	run := func(mode string) map[string]string {
		defer SetLagQCapEarlyForTest(mode)()
		// The announcement of the failed write (it failed before its
		// transaction began) must come down too.
		defer SetLagWriteAnnounceForTest(mode)()
		var fired, overlapped atomic.Bool
		joined := make(chan struct{}, 16)
		lagWriteJoinForTest = func(n uint64) {
			if n == failAt {
				select {
				case joined <- struct{}{}:
				default:
				}
			}
		}
		defer func() { lagWriteJoinForTest = nil }()
		lagWriteBeforeCommitForTest = func(n uint64) error {
			if n != failAt || fired.Load() {
				time.Sleep(10 * time.Millisecond)
				return nil
			}
			fired.Store(true)
			select {
			case <-joined:
				overlapped.Store(true)
			case <-time.After(10 * time.Second):
			}
			return errLagWriteInjected
		}
		defer func() { lagWriteBeforeCommitForTest = nil }()
		var tables map[string]string
		lagPipeTablesSinkForTest = func(piped *lagChain) {
			tables = dumpTables(t, piped.db)
		}
		defer func() { lagPipeTablesSinkForTest = nil }()
		c0, _, v0 := LagQCapEarlyStats()
		testLagPipelineAheadEngine(t, false, arrivalBeforeSnapshot, true)
		c1, tl1, v1 := LagQCapEarlyStats()
		t.Logf("early=%q: joined while failing %v, early captures %d (tails %d)", mode, overlapped.Load(), c1-c0, tl1)
		require.True(t, fired.Load(), "the injected failure did not fire")
		require.True(t, overlapped.Load(), "the failing write was not joined by the next hand-off")
		require.Zero(t, v1-v0)
		if mode != "" {
			require.GreaterOrEqual(t, c1-c0, uint64(8))
		}
		require.NotEmpty(t, tables)
		require.Eventually(t, func() bool { return lagBGGate.announced.Load() == 0 }, 5*time.Second, time.Millisecond,
			"an announcement stayed up after the failed write")
		return tables
	}
	want := run("")
	requireSameTables(t, want, run("1"), "early capture on, failed write")
	requireSameTables(t, want, run("check"), "early capture check, failed write")
}

func TestParseLagQCapEarly(t *testing.T) {
	require.Equal(t, lagQCapEarlyOff, parseLagQCapEarly(""))
	require.Equal(t, lagQCapEarlyOff, parseLagQCapEarly("0"))
	require.Equal(t, lagQCapEarlyOn, parseLagQCapEarly("1"))
	require.Equal(t, lagQCapEarlyCheck, parseLagQCapEarly("check"))
}
