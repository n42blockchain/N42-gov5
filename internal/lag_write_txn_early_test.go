// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestLagWriterTxnEarlySameRows (N42_LAG_WRITE_TXN_EARLY, with and without
// N42_STATE_WRITE_PREP_LAZY_LOG): block by block, a chain whose ordered
// writer opens the transaction before the state preparation is done stores
// exactly the rows of the default order -- every table -- with the history
// index deferred (prepared rows, and a replay for the SELFDESTRUCT block)
// and inline (replay).
func TestLagWriterTxnEarlySameRows(t *testing.T) {
	for _, deferred := range []bool{true, false} {
		for _, lazy := range []bool{false, true} {
			name := map[bool]string{true: "historyDeferred", false: "historyInline"}[deferred] +
				map[bool]string{true: "/lazyLog", false: ""}[lazy]
			t.Run(name, func(t *testing.T) {
				defer block.SetDeferredDepthForTest(2, 0)()
				defer SetLagWriteAsyncForTest(true)()
				defer SetLagWritePrepareForTest(true)()
				defer state.SetHistoryIndexDeferredForTest(deferred)()
				keys := frametest.Keys(t, 3, 71)
				const n = 8

				restore := SetLagWriteTxnEarlyForTest(false)
				ref := newLagChainEngine(t, keys, importBenchEngine{})
				blks, want := preparedWriteBlocks(t, ref, n)
				restore()

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
				defer state.SetPrepLazyLogForTest(lazy)()
				// Slow preparations would be the common case; a slow write
				// makes every child's hand-off wait as well.
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
}

// TestLagWriterTxnEarlyPipelineSameRows: the ordered-writer pipeline harness
// (every block executed ahead, writes handed off while the parent's is in
// flight, one injected import failure) ends with the same rows in every
// table with the early transaction on as off; the "wide" runs take the
// sharded, parallel-load preparation started in the root, with the lazy
// merged log.
func TestLagWriterTxnEarlyPipelineSameRows(t *testing.T) {
	cases := []struct {
		name string
		wide bool
	}{{"narrow", false}}
	if !testing.Short() {
		cases = append(cases, struct {
			name string
			wide bool
		}{"wide", true})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				prevF, prevP := lagPipeFramesForTest, lagPipePerFrameForTest
				lagPipeFramesForTest, lagPipePerFrameForTest = 17, 500
				defer func() { lagPipeFramesForTest, lagPipePerFrameForTest = prevF, prevP }()
				lagPipeDistinctToForTest = true
				defer func() { lagPipeDistinctToForTest = false }()
				defer state.SetFoldNoJournalForTest(true)()
				defer state.SetPrepLoadParForTest(8)()
				defer state.SetPrepShardsForTest(8)()
				defer SetLagWriteEarlyPrepForTest(true)()
			}
			defer state.SetHistoryIndexDeferredForTest(true)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetLagWriteEarlyPrepDelayForTest(false, 0)()
			lagWriteBeforeCommitForTest = func(uint64) error {
				time.Sleep(20 * time.Millisecond)
				return nil
			}
			defer func() { lagWriteBeforeCommitForTest = nil }()
			var (
				mu    sync.Mutex
				early int
			)
			blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
				mu.Lock()
				if ph.TxnEarly {
					early++
				}
				mu.Unlock()
			}
			defer func() { blockWritePhasesHook = nil }()
			run := func(on bool) map[string]string {
				defer SetLagWriteTxnEarlyForTest(on)()
				defer state.SetPrepLazyLogForTest(on && c.wide)()
				var tables map[string]string
				lagPipeTablesSinkForTest = func(piped *lagChain) { tables = dumpTables(t, piped.db) }
				defer func() { lagPipeTablesSinkForTest = nil }()
				mu.Lock()
				early = 0
				mu.Unlock()
				testLagPipelineAheadEngine(t, false, arrivalBeforeSnapshot, true)
				mu.Lock()
				defer mu.Unlock()
				if on {
					require.GreaterOrEqual(t, early, 8, "writes that took the early transaction")
				} else {
					require.Zero(t, early)
				}
				require.NotEmpty(t, tables)
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "early transaction")
		})
	}
}

// TestLagWriterTxnEarlyCrashMidTransaction: the early transaction of block
// 4 has put the block, its evidence, its QMDB rows and markers, its
// executed result and its receipts when the node "crashes" (the transaction
// never commits). The store is exactly the store after block 3 -- every
// table -- the chain restarts from it, block 4 imports again to the root
// it had in memory, and the chain goes on.
func TestLagWriterTxnEarlyCrashMidTransaction(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetLagWriteTxnEarlyForTest(true)()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	a1 := lc.addrs[1]

	const lost = 4
	reached := make(chan map[string]string, 1)
	release := make(chan struct{})
	lagWriteTxnEarlyMidForTest = func(n uint64) error {
		if n != lost {
			return nil
		}
		// The committed store while block 4's rows are put but not
		// committed (a reader on another goroutine: the writer's thread
		// holds the write transaction).
		done := make(chan map[string]string)
		go func() { done <- dumpTables(t, lc.db) }()
		reached <- <-done
		<-release
		return errLagWriteInjected
	}
	defer func() { lagWriteTxnEarlyMidForTest = nil }()

	g := lc.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	parent := g
	for n := uint64(1); n <= lost; n++ {
		if n >= 3 {
			waitLagWritten(t, lc.bc, n-2)
		}
		b := lc.build(parent, 0, lc.transfer(0, n-1, a1, 1000+n), lc.transfer(1, n-1, a1, 7))
		lc.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
	}
	var before map[string]string
	select {
	case before = <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("block 4's early transaction never reached its state rows")
	}
	waitLagWritten(t, lc.bc, lost-1)
	job := lc.bc.lagWritePending(blks[lost-1].Hash())
	require.NotNil(t, job, "block 4's write is not in flight")
	memRoot, ok := job.ibs.LastIntermediateRoot()
	require.True(t, ok)

	close(release)
	<-job.done
	lagWriteTxnEarlyMidForTest = nil
	require.Error(t, job.err)
	require.NoError(t, lc.bc.Close())

	// Nothing of block 4 reached the store.
	requireSameTables(t, before, dumpTables(t, lc.db), "store after the crash")
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, uint64(lost-1), an, "applied marker")
		require.Equal(t, blks[lost-2].Hash(), types.Hash(ah))
		require.Nil(t, rawdb.ReadHeader(tx, blks[lost-1].Hash(), lost), "the lost block's header is stored")
		_, found, err := rawdb.ReadExecutedResult(tx, blks[lost-1].Hash())
		require.NoError(t, err)
		require.False(t, found, "the lost block's executed result is stored")
		v, err := tx.GetOne(modules.QMDBUndoWindow, modules.EncodeBlockNumber(lost))
		require.NoError(t, err)
		require.Nil(t, v, "the lost block's undo row is stored")
		return nil
	}))

	re := reopenLagChain(t, lc)
	require.Equal(t, re.result(blks[lost-2].Hash()).Root, re.bc.qmdbRootComputer.Root(), "reloaded tree root")
	re.bc.RememberAvailable(blks[lost-1])
	re.bc.EnqueueLagImport(blks[lost-1])
	waitLagWritten(t, re.bc, lost)
	require.Equal(t, memRoot, re.result(blks[lost-1].Hash()).Root, "re-imported root")
	b5 := re.build(blks[lost-1].Header().(*block.Header), 0, re.transfer(0, lost, a1, 1))
	re.bc.EnqueueLagImport(b5)
	waitLagWritten(t, re.bc, lost+1)
	b6 := re.build(b5.Header().(*block.Header), 0, re.transfer(0, lost+1, a1, 1))
	require.Equal(t, memRoot, b6.Header().(*block.Header).Root)
	re.bc.EnqueueLagImport(b6)
	waitLagWritten(t, re.bc, lost+2)
	re.bc.lock.Lock()
	require.NoError(t, re.bc.joinLagWrite())
	re.bc.lock.Unlock()
	require.Equal(t, re.result(b6.Hash()).Root, re.bc.qmdbRootComputer.Root())
}

// TestLagWriterTxnEarlyFailedMidTransactionRequeues: a write that fails
// inside its early transaction (rows put, none committed) is recovered as
// any failed write -- the tree reloaded, the block queued again -- and the
// chain ends with the roots and the tables of a chain whose writes never
// failed.
func TestLagWriterTxnEarlyFailedMidTransactionRequeues(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	const blocks = 6
	// The reference builds the blocks with the default order and no failure.
	ref := newLagChainEngine(t, keys, importBenchEngine{})
	parent := ref.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	for n := uint64(1); n <= blocks; n++ {
		if n >= 3 {
			waitLagWritten(t, ref.bc, n-2)
		}
		b := ref.build(parent, 0, ref.transfer(0, n-1, ref.addrs[1], 10*n), ref.transfer(2, n-1, ref.addrs[0], n))
		ref.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
	}
	waitLagWritten(t, ref.bc, blocks)
	ref.bc.lock.Lock()
	require.NoError(t, ref.bc.joinLagWrite())
	ref.bc.lock.Unlock()
	want := dumpTables(t, ref.db)

	// The same blocks, early transaction on, block 3's write failing once
	// with part of its rows put.
	defer SetLagWriteTxnEarlyForTest(true)()
	fired := 0
	lagWriteTxnEarlyMidForTest = func(n uint64) error {
		if n == 3 && fired == 0 {
			fired++
			return errors.New("injected mid-transaction failure")
		}
		return nil
	}
	defer func() { lagWriteTxnEarlyMidForTest = nil }()
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	for _, b := range blks {
		lc.bc.RememberAvailable(b)
		lc.bc.EnqueueLagImport(b)
	}
	waitLagWritten(t, lc.bc, blocks)
	lc.bc.lock.Lock()
	require.NoError(t, lc.bc.joinLagWrite())
	lc.bc.lock.Unlock()
	_, failed, _ := lc.bc.LagWriteStats()
	require.Equal(t, 1, fired)
	require.Equal(t, uint64(1), failed, "the injected failure did not fail the write")
	for _, b := range blks {
		require.Equal(t, ref.result(b.Hash()).Root, lc.result(b.Hash()).Root, "block %d", b.Number64().Uint64())
	}
	requireSameTables(t, want, dumpTables(t, lc.db), "after the recovered failure")
}

// waitSplit attributes a wait for a write to the phases the write was in
// while it lasted, whatever the order the phases were entered in.
func TestLagWriteJobWaitSplit(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	j := &lagWriteJob{}
	j.marks[lwpStart] = at(0)
	j.marks[lwpPrepWait] = at(1)
	j.marks[lwpBegin] = at(5)
	// TXN_EARLY: body, then the in-transaction wait, then body again (the
	// later stamp wins), then the changesets.
	j.marks[lwpPrepWaitTxn] = at(10)
	j.marks[lwpBody] = at(18)
	j.marks[lwpChgset] = at(20)
	j.marks[lwpCommit] = at(30)
	j.marks[lwpPost] = at(45)
	j.end = at(46)
	sp := j.waitSplit(at(12))
	require.Equal(t, 6*time.Millisecond, sp[lwpPrepWaitTxn])
	require.Equal(t, 2*time.Millisecond, sp[lwpBody])
	require.Equal(t, 10*time.Millisecond, sp[lwpChgset])
	require.Equal(t, 15*time.Millisecond, sp[lwpCommit])
	require.Equal(t, time.Millisecond, sp[lwpPost])
	require.Zero(t, sp[lwpBegin])
	var sum time.Duration
	for _, d := range sp {
		sum += d
	}
	require.Equal(t, 34*time.Millisecond, sum)
	require.Equal(t, [lwpCount]time.Duration{}, j.waitSplit(at(50)), "a wait after the end")
	require.Equal(t, [lwpCount]time.Duration{}, (*lagWriteJob)(nil).waitSplit(at(0)))
}
