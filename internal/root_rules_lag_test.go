// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// rootRulesLagRun is one pipeline harness run for TestRootRulesLagPipeline.
type rootRulesLagRun struct {
	results            map[uint64]rawdb.ExecutedResult
	pipedHist, serHist map[string]string
	pipedBroken        bool
	serialHanded       uint64
	// coinbaseVersions: how many times the serial chain's QMDB tree
	// appended the coinbase's account leaf (its history journal's versions).
	coinbaseVersions int
	// movedWrites: the ordered writes that ran with the child's root
	// already on the live tree.
	movedWrites int
}

// rootRulesCoinbase is the lag harness's block coinbase.
var rootRulesCoinbase = types.HexToAddress("0x1111111111111111111111111111111111111111")

// runRootRulesLag runs the ordered-writer pipeline harness (every child's
// root while its parent's write is in flight, one injected write failure)
// with both root rules (UnchangedAccountNoOpTime, StorageNetChangeTime)
// active from genesis in both chains' configs and the QMDB history journal
// on in both. Only the first block pays a priority fee: from the second on
// the coinbase is credited zero and ends every block unchanged. The serial
// reference chain imports through the plain path (no parallel EVM, no
// ordered writer: every write synchronous), where that zero credit dirties
// the coinbase; the pipelined chain's Block-STM fold never does.
func runRootRulesLag(t *testing.T, levers bool) rootRulesLagRun {
	t.Helper()
	var out rootRulesLagRun
	lagChainConfigForTest = func(cfg *params.ChainConfig) {
		cfg.UnchangedAccountNoOpTime = big.NewInt(0)
		cfg.StorageNetChangeTime = big.NewInt(0)
	}
	var piped *BlockChain
	lagChainSetupForTest = func(lc *lagChain) {
		lc.bc.EnableQMDBHistory(false)
		require.NoError(t, lc.bc.prepareQMDBHistory())
		if piped == nil {
			piped = lc.bc // the harness makes the pipelined chain first
		}
	}
	defer func() { lagChainConfigForTest, lagChainSetupForTest = nil, nil }()
	if levers {
		defer r68aWideFlags()()
		defer SetLagWritePrepareForTest(true)()
		defer SetLagHandoffLeanForTest(true)()
	} else {
		lagPipeDistinctToForTest = true
		defer func() { lagPipeDistinctToForTest = false }()
	}
	prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
	lagPipeFramesForTest, lagPipePerFrameForTest = 6, 400 // 2400 credits: the detached walk
	defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
	const perBlock = 6 * 400
	lagPipeTipFnForTest = func(n uint64) uint64 {
		if n < perBlock {
			return 1 // the first block creates the coinbase
		}
		return 0
	}
	defer func() { lagPipeTipFnForTest = nil }()
	defer SetLagWriteAsyncForTest(true)()
	// Each ordered write waits (bounded) right before it journals its block
	// until the child's root is on the live tree: the journal is then
	// written for a block the tree has moved past.
	var moved int
	lagWriteBeforeJournalForTest = func(j *lagWriteJob) {
		if j.number == 4 || j.number == 5 {
			// Around the harness's injected failure of block 5 (failAt):
			// left as its checks expect.
			return
		}
		end := j.undo.PrevNextSlot + uint64(len(j.undo.AppendedKeys))
		for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if piped.qmdbRootComputer.NextSlot() != end {
				moved++
				return
			}
		}
	}
	defer func() { lagWriteBeforeJournalForTest = nil }()
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()

	out.results = map[uint64]rawdb.ExecutedResult{}
	lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { out.results[n] = r }
	historyTables := func(lc *lagChain) (map[string]string, bool) {
		all := dumpTables(t, lc.db)
		h := map[string]string{}
		for _, name := range []string{modules.QMDBBlockPos, modules.QMDBTopBand, modules.QMDBKeyVers, modules.QMDBDeathStamps} {
			h[name] = all[name]
		}
		var broken bool
		require.NoError(t, lc.db.View(lc.bc.ctx, func(tx kv.Tx) error {
			_, ok, err := commitment.ReadQMDBHistoryBroken(tx)
			broken = ok
			return err
		}))
		return h, broken
	}
	lagPipeEngineDoneForTest = func(piped *lagChain) {
		out.pipedHist, out.pipedBroken = historyTables(piped)
	}
	lagPipeSerialDoneForTest = func(serial *lagChain) {
		var broken bool
		out.serHist, broken = historyTables(serial)
		require.False(t, broken, "the serial chain's QMDB history journal is marked broken")
		out.serialHanded, _, _ = serial.bc.LagWriteStats()
		require.NoError(t, serial.db.View(serial.bc.ctx, func(tx kv.Tx) error {
			c, err := tx.CursorDupSort(modules.QMDBKeyVers)
			if err != nil {
				return err
			}
			defer c.Close()
			key := commitment.AccountKeyHash(rootRulesCoinbase)
			_, v, err := c.SeekExact(key[:])
			for ; err == nil && v != nil; _, v, err = c.NextDup() {
				out.coinbaseVersions++
			}
			return err
		}))
	}
	defer func() {
		lagPipeResultSinkForTest, lagPipeEngineDoneForTest, lagPipeSerialDoneForTest = nil, nil, nil
	}()
	testLagPipelineAheadEngine(t, false, arrivalMidImport, true)
	out.movedWrites = moved
	return out
}

// TestRootRulesLagPipeline: with UnchangedAccountNoOpTime and
// StorageNetChangeTime set (unset in every shipped config), a block sequence
// through the lag pipeline -- ahead runs, the ordered writer, and with the
// finalize/fold/hoist/prepared-write levers on or off -- reaches the same
// executed results as the serial chain's plain import (the harness checks
// every block) while the plain import credits the unchanged coinbase every
// block (without the rule it would write its leaf every block and the two
// paths' roots would part). The QMDB history journal the ordered writer
// records -- each block written while its child's root may already be on
// the live tree -- equals the plain import's row for row and is never
// marked broken.
func TestRootRulesLagPipeline(t *testing.T) {
	defer state.SetFinalizeFastMinForTest(64)()
	on := runRootRulesLag(t, true)
	plain := runRootRulesLag(t, false)
	for _, r := range []rootRulesLagRun{on, plain} {
		require.Zero(t, r.serialHanded, "the serial reference used the ordered writer")
		require.False(t, r.pipedBroken, "the pipelined chain's QMDB history journal is marked broken")
		require.NotEmpty(t, r.pipedHist[modules.QMDBBlockPos])
		require.GreaterOrEqual(t, r.movedWrites, 8, "writes that ran with the child's root on the live tree")
		require.Equal(t, r.serHist, r.pipedHist, "QMDB history journal: ordered writer vs plain import")
		// Genesis has no coinbase leaf; the first block writes it, and the
		// rule keeps every later block's zero credit out of the tree.
		require.Equal(t, 1, r.coinbaseVersions, "coinbase leaf versions on the plain import")
	}
	require.Equal(t, on.results, plain.results, "levers on and off differ")
}
