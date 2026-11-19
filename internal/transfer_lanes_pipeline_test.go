// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// lanesPipelineRun is one run of the ordered-writer pipeline harness.
type lanesPipelineRun struct {
	results  map[uint64]rawdb.ExecutedResult
	tables   map[string]string
	used     uint64
	declined uint64
	checked  uint64
	// earlyPub, earlyUsed: early snapshots published and used.
	earlyPub, earlyUsed uint64
}

// runLanesPipeline runs the lag pipeline harness (frame blocks executed
// ahead on Block-STM, the ordered writer, an injected write failure, every
// block checked against a serial chain's plain import) with
// N42_EXEC_TRANSFER_LANES = mode. rules sets UnchangedAccountNoOpTime and
// StorageNetChangeTime from genesis, sends every transfer of a block to its
// own account (created by the first block) and pays a priority fee only in
// the first block; without rules every transfer goes to one recipient.
// early publishes each block's final writes as an early snapshot its child
// starts on (N42_LAG_EARLY_SNAPSHOT, deferred depth 3, recipients the
// parent did not write).
func runLanesPipeline(t *testing.T, mode string, rules, early bool) lanesPipelineRun {
	t.Helper()
	var out lanesPipelineRun
	defer SetTransferLanesForTest(mode)()
	if rules {
		lagChainConfigForTest = func(cfg *params.ChainConfig) {
			cfg.UnchangedAccountNoOpTime = big.NewInt(0)
			cfg.StorageNetChangeTime = big.NewInt(0)
		}
		defer func() { lagChainConfigForTest = nil }()
		lagPipeDistinctToForTest = true
		defer func() { lagPipeDistinctToForTest = false }()
		prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
		lagPipeFramesForTest, lagPipePerFrameForTest = 4, 300
		defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
		lagPipeTipFnForTest = func(n uint64) uint64 {
			if n < 4*300 {
				return 1
			}
			return 0
		}
		defer func() { lagPipeTipFnForTest = nil }()
	}
	arrival := arrivalBeforeSnapshot
	if early {
		defer SetLagEarlySnapshotForTest(true)()
		lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
		defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
		lagPipeAheadClaimedForTest = waitOwnChildForTest
		defer func() { lagPipeAheadClaimedForTest = nil }()
		arrival = arrivalAny
		lagPipeEngineDoneForTest = func(piped *lagChain) {
			s := &piped.bc.lagPipe.earlyStats
			out.earlyPub, out.earlyUsed = s.published.Load(), s.used.Load()
		}
		defer func() { lagPipeEngineDoneForTest = nil }()
	}
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	out.results = map[uint64]rawdb.ExecutedResult{}
	lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { out.results[n] = r }
	lagPipeTablesSinkForTest = func(piped *lagChain) { out.tables = dumpTables(t, piped.db) }
	defer func() { lagPipeResultSinkForTest, lagPipeTablesSinkForTest = nil, nil }()
	u0, d0, c0, m0 := TransferLanesStats()
	testLagPipelineAheadEngine(t, false, arrival, true)
	u1, d1, c1, m1 := TransferLanesStats()
	require.Equal(t, m0, m1, "check mode found a difference")
	out.used, out.declined, out.checked = u1-u0, d1-d0, c1-c0
	require.NotEmpty(t, out.tables)
	return out
}

// TestTransferLanesLagPipeline: chains of frame blocks through the real
// ahead/lag/import pipeline, with the root rules (UnchangedAccountNoOp,
// StorageNetChange) on and off, end with the same executed results (each
// equal to the serial chain's plain import, block by block) and the same
// rows in every table with the lanes off, on and checking; the lanes take
// every block whose recipients exist in its base. The earlySnapshot case
// starts each child on its parent's final writes, which the lanes published.
func TestTransferLanesLagPipeline(t *testing.T) {
	for _, c := range []struct {
		name         string
		rules, early bool
	}{{"rulesOff", false, false}, {"rulesOn", true, false}, {"earlySnapshot", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			off := runLanesPipeline(t, "off", c.rules, c.early)
			require.Zero(t, off.used+off.declined+off.checked, "the lanes ran with the switch off")
			on := runLanesPipeline(t, "on", c.rules, c.early)
			t.Logf("on: lanes used %d, declined %d, early snapshots published %d, used %d", on.used, on.declined, on.earlyPub, on.earlyUsed)
			require.GreaterOrEqual(t, on.used, uint64(8), "the lanes took few blocks")
			if c.early {
				require.GreaterOrEqual(t, on.earlyUsed, uint64(4), "few children started on a lane block's early snapshot")
			}
			check := runLanesPipeline(t, "check", c.rules, c.early)
			t.Logf("check: compared %d, declined %d", check.checked, check.declined)
			require.Zero(t, check.used, "check mode used the lanes' result")
			require.Equal(t, on.used, check.checked, "check mode compared other blocks than the lanes took")
			require.Equal(t, off.results, on.results, "executed results, lanes on")
			require.Equal(t, off.results, check.results, "executed results, lanes check")
			requireSameTables(t, off.tables, on.tables, "lanes on")
			requireSameTables(t, off.tables, check.tables, "lanes check")
		})
	}
}
