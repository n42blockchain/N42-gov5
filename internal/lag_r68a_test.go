// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// r68aWideFlags sets the bench's state flags for the "wide" runs (see
// TestLagWriterEarlyPrepSameRows) and returns the restore.
func r68aWideFlags() func() {
	prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
	lagPipeFramesForTest, lagPipePerFrameForTest = 17, 500
	lagPipeDistinctToForTest = true
	restores := []func(){
		state.SetFinalizeFastForTest(true),
		state.SetFinalizeFastMinForTest(64),
		state.SetFoldNoJournalForTest(true),
		state.SetFinalizeHoistForTest(true),
		state.SetPrehookCutForTest(1),
		state.SetPrepLoadParForTest(8),
		state.SetPrepShardsForTest(8),
	}
	return func() {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
		lagPipeDistinctToForTest = false
		lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP
	}
}

type r68aCase struct {
	name     string
	deferred bool
	wide     bool
}

func r68aCases() []r68aCase {
	cases := []r68aCase{{"historyDeferred", true, false}, {"historyInline", false, false}}
	if !testing.Short() {
		cases = append(cases, r68aCase{"wide/historyDeferred", true, true}, r68aCase{"wide/historyInline", false, true})
	}
	return cases
}

// r68aRunTables runs the ordered-writer pipeline harness (every block
// executed ahead, every write handed off, one injected write failure) with
// slow writes and returns every table of the pipelined chain.
func r68aRunTables(t *testing.T) map[string]string {
	t.Helper()
	var tables map[string]string
	lagPipeTablesSinkForTest = func(piped *lagChain) { tables = dumpTables(t, piped.db) }
	defer func() { lagPipeTablesSinkForTest = nil }()
	testLagPipelineAheadEngine(t, false, arrivalBeforeSnapshot, true)
	require.NotEmpty(t, tables)
	return tables
}

// TestLagHandoffLeanSameRows (r68a, N42_LAG_HANDOFF_LEAN): the hand-off's
// eviction and FlushTo run off the importing goroutine. The harness's
// chains with the flag off and on must end with exactly the same rows in
// every table (the QMDB entry, twig and leaf tables, the undo rows, the
// applied marker and executed results included), with the history index
// deferred and inline; the lean capture must have run for the handed-off
// blocks.
func TestLagHandoffLeanSameRows(t *testing.T) {
	for _, c := range r68aCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				defer r68aWideFlags()()
			}
			defer state.SetHistoryIndexDeferredForTest(c.deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			lagWriteBeforeCommitForTest = func(uint64) error {
				time.Sleep(30 * time.Millisecond)
				return nil
			}
			defer func() { lagWriteBeforeCommitForTest = nil }()
			run := func(on bool) map[string]string {
				defer SetLagHandoffLeanForTest(on)()
				c0 := lagLeanCaptures.Load()
				tables := r68aRunTables(t)
				n := lagLeanCaptures.Load() - c0
				t.Logf("lean=%v: lean captures %d", on, n)
				if on {
					require.GreaterOrEqual(t, n, uint64(8), "hand-offs captured off the importing goroutine")
				} else {
					require.Zero(t, n)
				}
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "lean hand-off")
		})
	}
}

// TestLagWriterEarlyPrepDelaySameRows (r68a, N42_LAG_WRITE_EARLY_PREP_DELAY):
// with the early preparation on, delaying its start to the child's launch
// (and running it at 3 shards) must leave exactly the rows the preparation
// at the hand-off leaves, and the early ones must still run.
func TestLagWriterEarlyPrepDelaySameRows(t *testing.T) {
	for _, c := range r68aCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				defer r68aWideFlags()()
			}
			defer state.SetHistoryIndexDeferredForTest(c.deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			lagWriteBeforeCommitForTest = func(uint64) error {
				time.Sleep(30 * time.Millisecond)
				return nil
			}
			defer func() { lagWriteBeforeCommitForTest = nil }()
			var (
				mu    sync.Mutex
				early int
				lead  time.Duration
			)
			blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
				mu.Lock()
				defer mu.Unlock()
				if ph.PrepEarly {
					early++
					lead += ph.PrepLead
				}
			}
			defer func() { blockWritePhasesHook = nil }()
			run := func(earlyOn, delay bool, shards int) map[string]string {
				defer SetLagWriteEarlyPrepForTest(earlyOn)()
				defer SetLagWriteEarlyPrepDelayForTest(delay, shards)()
				mu.Lock()
				early, lead = 0, 0
				mu.Unlock()
				d0, e0 := lagEarlyPrepDelayed.Load(), lagEarlyPreps.Load()
				tables := r68aRunTables(t)
				byChild, started := lagEarlyPrepDelayed.Load()-d0, lagEarlyPreps.Load()-e0
				mu.Lock()
				defer mu.Unlock()
				t.Logf("early=%v delay=%v shards=%d: started %d (by the child's launch %d), %d writes early, mean lead %v",
					earlyOn, delay, shards, started, byChild, early, lead/time.Duration(max(early, 1)))
				if earlyOn {
					require.GreaterOrEqual(t, early, 8, "writes whose preparation started early")
				} else {
					require.Zero(t, started)
				}
				if !delay {
					require.Zero(t, byChild)
				}
				return tables
			}
			want := run(false, false, 0)
			requireSameTables(t, want, run(true, false, 0), "early preparation")
			requireSameTables(t, want, run(true, true, 0), "delayed early preparation")
			requireSameTables(t, want, run(true, true, 3), "delayed early preparation at 3 shards")
		})
	}
}

// foldCut is shardFoldBlock.fold with the no-journal fold switchable (as
// runParallelMode sets it around the fold).
func (b *shardFoldBlock) foldCut(t *testing.T, shard int, nj bool) shardFoldResult {
	t.Helper()
	prev := collectShardWorkers
	collectShardWorkers = shard
	defer func() { collectShardWorkers = prev }()
	mk := func() *state.IntraBlockState {
		pf := state.NewAccountPrefetch(b.store)
		pf.SetKnown(b.known)
		ibs := state.New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.SetDetachedPendingCredits(true)
		ibs.GetBalance(b.live)
		ibs.AddBalance(b.credited, uint256.NewInt(5))
		ibs.SetFoldNoJournal(nj)
		err := applyMVSToIBS(b.mvs, b.numTxs, ibs)
		ibs.SetFoldNoJournal(false)
		require.NoError(t, err)
		return ibs
	}
	rules := &params.Rules{IsSpuriousDragon: true}
	var res shardFoldResult
	ibs := mk()
	require.NoError(t, ibs.FinalizeTx(rules, state.NewNoopWriter()))
	for _, a := range ibs.DirtyAddresses() {
		res.dirty = append(res.dirty, a.Hex())
	}
	sort.Strings(res.dirty)
	rc := &hashingRootComputer{}
	ibs.SetRootComputer(rc)
	res.root = ibs.IntermediateRoot()
	res.rootRows = append([]string(nil), rc.rows...)

	ibs2 := mk()
	require.NoError(t, ibs2.FinalizeTx(rules, state.NewNoopWriter()))
	w := &recWriter{}
	require.NoError(t, ibs2.CommitBlock(rules, w))
	sort.Strings(w.log)
	res.rows = w.log
	return res
}

// TestApplyMVSCollectCutMatches (r68a, N42_AHEAD_COLLECT_CUT): the fold with
// its objects from a slab and its walk at more workers leaves the same
// dirty set, root and committed rows as without, over random blocks, with
// the serial and the sharded account pass, journalled and not.
func TestApplyMVSCollectCutMatches(t *testing.T) {
	blocks := 8
	if testing.Short() {
		blocks = 4
	}
	for i := 0; i < blocks; i++ {
		numTxs := 9000 + 3000*(i%4)
		b := newShardFoldBlock(int64(100+i), numTxs, i%2 == 1)
		for _, shard := range []int{0, 16} {
			for _, nj := range []bool{false, true} {
				off := func() shardFoldResult {
					defer state.SetFoldObjectSlabForTest(false)()
					return b.foldCut(t, shard, nj)
				}()
				on := func() shardFoldResult {
					defer state.SetFoldObjectSlabForTest(true)()
					require.GreaterOrEqual(t, applyMVSWorkers(numTxs), applyMVSWorkersOff(numTxs))
					return b.foldCut(t, shard, nj)
				}()
				require.GreaterOrEqual(t, len(off.rows), numTxs)
				require.Equal(t, off.root, on.root, "block %d shard %d nj %v: root", i, shard, nj)
				require.Equal(t, off.dirty, on.dirty, "block %d shard %d nj %v: dirty set", i, shard, nj)
				require.Equal(t, off.rootRows, on.rootRows, "block %d shard %d nj %v: root dirty set", i, shard, nj)
				require.Equal(t, off.rows, on.rows, "block %d shard %d nj %v: committed rows", i, shard, nj)
			}
		}
	}
}

// applyMVSWorkersOff is applyMVSWorkers with the cut off.
func applyMVSWorkersOff(numTxs int) int {
	defer state.SetFoldObjectSlabForTest(false)()
	return applyMVSWorkers(numTxs)
}

// TestAheadCollectCutSameRows (r68a, N42_AHEAD_COLLECT_CUT): the ordered-
// writer pipeline harness with the cut off and on, the fold's sharded
// account pass on, must end with exactly the same rows in every table.
func TestAheadCollectCutSameRows(t *testing.T) {
	for _, c := range r68aCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				defer r68aWideFlags()()
			}
			prev := collectShardWorkers
			collectShardWorkers = 16
			defer func() { collectShardWorkers = prev }()
			defer state.SetHistoryIndexDeferredForTest(c.deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			run := func(on bool) map[string]string {
				defer state.SetFoldObjectSlabForTest(on)()
				return r68aRunTables(t)
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "collect cut")
		})
	}
}

// TestR68aAllFlagsSameRows: all three r68a flags on at once (with the early
// preparation) against all off.
func TestR68aAllFlagsSameRows(t *testing.T) {
	if testing.Short() {
		t.Skip("wide pipeline run")
	}
	defer r68aWideFlags()()
	prev := collectShardWorkers
	collectShardWorkers = 16
	defer func() { collectShardWorkers = prev }()
	defer state.SetHistoryIndexDeferredForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	run := func(on bool) map[string]string {
		defer SetLagHandoffLeanForTest(on)()
		defer SetLagWriteEarlyPrepForTest(on)()
		defer SetLagWriteEarlyPrepDelayForTest(on, 4)()
		defer state.SetFoldObjectSlabForTest(on)()
		return r68aRunTables(t)
	}
	want := run(false)
	got := run(true)
	requireSameTables(t, want, got, "all r68a flags")
}
