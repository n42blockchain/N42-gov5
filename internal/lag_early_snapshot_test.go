// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/stretchr/testify/require"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/lib/kv/memdb"
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
		// fleet: the E1N26 bench's switches on top (ordered writer with its
		// prepared and early-prepared writes, lean hand-off, early publish,
		// ahead prep, lean exec, fast partition, and the finalize levers).
		fleet bool
		// retry: N42_LAG_EARLY_RETRY on (else the environment's).
		retry bool
	}{{"inline", false, false, false, false}, {"orderedWriter", true, false, false, false}, {"writeAfterFold", false, true, false, false},
		{"fleetFlags", true, false, true, false}, {"fleetFlagsWriteAfterFold", true, true, true, false},
		{"retry", false, false, false, true}, {"fleetFlagsRetry", true, false, true, true},
		{"fleetFlagsRetryWriteAfterFold", true, true, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			if tc.retry {
				defer SetLagEarlyRetryForTest(true)()
			}
			if tc.fleet {
				defer SetLagWritePrepareForTest(true)()
				defer SetLagWriteEarlyPrepForTest(true)()
				defer SetLagHandoffLeanForTest(true)()
				defer SetLagWriteDeferBGForTest(true)()
				defer SetLagEarlyPublishForTest(true)()
				defer SetAheadPrepForTest(true)()
				defer parallel.SetExecLeanForTest(true)()
				defer parallel.SetWavePartitionFastForTest(true)()
				defer state.SetFinalizeFastForTest(true)()
				defer state.SetFoldNoJournalForTest(true)()
				defer state.SetPrehookCutForTest(1)()
				defer state.SetFinalizeHoistForTest(true)()
				defer state.SetFoldObjectSlabForTest(true)()
			}
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
	require.NotZero(t, held.heldNanos.Load(), "the held read's wait was not recorded")
}

// TestEarlySnapshotKeepsStoreAfterRun: an ahead run whose early snapshot
// took the executor's store returns without releasing it -- the child's run
// reads the store after its parent's run returned, until the parent's root
// hook -- and the snapshot's release hands it back, once. A run whose
// snapshot was declined releases the store itself. (The run's deferred
// release used to hand a taken store's version lists back for reuse under
// the child's reads: the child read cleared or reused versions and the
// block's result differed from the serial chain's.)
func TestEarlySnapshotKeepsStoreAfterRun(t *testing.T) {
	const txs, senders = 2000, 100
	fx := newTransferBlockFixture(txs, senders, 300, 0, 0)
	db := memdb.NewTestDB(t)
	fundTransferBlockStateFiller(t, db, fx, 100)
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	run := func(take bool) {
		tx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		defer tx.Rollback()
		var mvs *parallel.MVS
		var numTxs int
		var free func()
		var pooled bool
		claim := &aheadClaim{early: func(m *parallel.MVS, n int, _ *state.PostState, f func()) bool {
			mvs, numTxs, free, pooled = m, n, f, m.HoldsArenaForTest()
			return take
		}}
		_, err = sp.executeParallelAhead(blk, newNJState(tx), hashFn, claim)
		require.NoError(t, err)
		require.NotNil(t, mvs, "the run did not offer its store")
		require.Equal(t, txs, numTxs)
		if !pooled {
			// N42_BLOCK_SCRATCH_POOL=0: the store holds nothing to release.
			t.Skip("the per-block scratch pool is off")
		}
		if !take {
			require.False(t, mvs.HoldsArenaForTest(), "a declined store was not released by its run")
			return
		}
		require.True(t, mvs.HoldsArenaForTest(), "the run released the store its early snapshot took")
		// The store still answers the block's final writes.
		full, _, _, found, _ := mvs.ReadAccount(parallel.LocationKey{Address: fx.senders[0], Field: parallel.FieldBalance}, numTxs)
		require.True(t, found, "the taken store lost a sender's final write")
		require.NotNil(t, full)
		acc, err := parallel.DecodeAccount(full)
		require.NoError(t, err)
		require.Equal(t, uint64(txs/senders), acc.Nonce)
		free()
		require.False(t, mvs.HoldsArenaForTest(), "the snapshot's release kept the store")
		free() // once
	}
	run(true)
	run(false)
}

// TestLagEarlyRetryParksUntilGrandparentHook forces the order the fleet
// refuses one early offer in nine with (N42_LAG_EARLY_RETRY off): block N's
// run, the next run on N-1's early snapshot, ends its Block-STM before
// N-1's root hook. Each hook of N-1 waits until N's run offered its
// snapshot. With the retry the offer is parked, published at N-1's hook on
// N-1's complete post-state, and N+1 launches there. Every block equals
// the serial chain's (the harness), and every early snapshot's store is
// released exactly once.
func TestLagEarlyRetryParksUntilGrandparentHook(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet bool
	}{{"inline", false}, {"fleetFlags", true}} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			defer SetLagEarlyRetryForTest(true)()
			// The parking path: a stacked snapshot (N42_LAG_EARLY_DEPTH2)
			// takes the offers this test parks.
			defer SetLagEarlyDepth2ForTest(false)()
			if tc.fleet {
				defer SetLagWriteAsyncForTest(true)()
				defer SetLagWritePrepareForTest(true)()
				defer SetLagWriteEarlyPrepForTest(true)()
				defer SetLagHandoffLeanForTest(true)()
				defer SetLagWriteDeferBGForTest(true)()
				defer SetLagEarlyPublishForTest(true)()
				defer SetAheadPrepForTest(true)()
				defer SetAheadPreEarlyForTest(true)()
				defer parallel.SetExecLeanForTest(true)()
				defer parallel.SetWavePartitionFastForTest(true)()
				defer state.SetFinalizeFastForTest(true)()
				defer state.SetFoldNoJournalForTest(true)()
				defer state.SetPrehookCutForTest(1)()
				defer state.SetFinalizeHoistForTest(true)()
				defer state.SetFoldObjectSlabForTest(true)()
			}
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()

			var piped atomic.Pointer[BlockChain]
			lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
				piped.Store(bc)
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if bc.lagQueuedChild(b.Hash()) != nil {
						return
					}
				}
			}
			defer func() { lagPipeAheadClaimedForTest = nil }()

			// Every early snapshot's references: the count left after each
			// drop, and how often it reached zero (the store's release).
			var refMu sync.Mutex
			released := map[*earlySnap]int{}
			negative := 0
			lagEarlyUnrefForTest = func(es *earlySnap, left int32) {
				refMu.Lock()
				defer refMu.Unlock()
				switch {
				case left == 0:
					released[es]++
				case left < 0:
					negative++
				default:
					if _, ok := released[es]; !ok {
						released[es] = 0
					}
				}
			}
			defer func() { lagEarlyUnrefForTest = nil }()

			// N-1's hook waits for N's run (the next run on N-1's early
			// snapshot) to end its Block-STM and offer.
			var forced atomic.Int32
			lagPipeBeforePublishForTest = func(h types.Hash) {
				bc := piped.Load()
				if bc == nil {
					return
				}
				p := &bc.lagPipe
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					p.mu.Lock()
					a, parked := p.next, p.parked != nil && p.parkedRun == p.next
					p.mu.Unlock()
					if a == nil || a.parent != h {
						return
					}
					if parked {
						forced.Add(1)
						return
					}
					select {
					case <-a.done:
						return
					default:
					}
				}
			}
			defer func() { lagPipeBeforePublishForTest = nil }()
			// After N-1's hook: N's snapshot is published and N+1 runs on it.
			var atGrand atomic.Int32
			lagPipeAfterPublishForTest = func(h types.Hash) {
				bc := piped.Load()
				if bc == nil {
					return
				}
				p := &bc.lagPipe
				p.mu.Lock()
				defer p.mu.Unlock()
				if es := p.early; es != nil && es.header.ParentHash == h && p.next != nil && p.next.es == es {
					atGrand.Add(1)
				}
			}
			defer func() { lagPipeAfterPublishForTest = nil }()

			var parked, parkedPub, parkedDrop, used uint64
			lagPipeEngineDoneForTest = func(lc *lagChain) {
				s := &lc.bc.lagPipe.earlyStats
				lc.bc.lagPipe.mu.Lock()
				require.Nil(t, lc.bc.lagPipe.next, "a next run outlived its blocks")
				require.Nil(t, lc.bc.lagPipe.parked, "a parked snapshot outlived its blocks")
				lc.bc.lagPipe.mu.Unlock()
				parked, parkedPub, parkedDrop, used = s.parked.Load(), s.parkedPublished.Load(), s.parkedDropped.Load(), s.used.Load()
			}
			defer func() { lagPipeEngineDoneForTest = nil }()

			testLagPipelineAheadEngine(t, false, arrivalAny, tc.fleet)
			t.Logf("forced %d, parked %d, published %d, dropped %d, launched at the grandparent's hook %d, early used %d",
				forced.Load(), parked, parkedPub, parkedDrop, atGrand.Load(), used)
			require.NotZero(t, forced.Load(), "no early offer came before its grandparent's hook")
			require.Equal(t, parked, parkedPub+parkedDrop, "a parked snapshot was neither published nor dropped")
			require.NotZero(t, parkedPub, "no parked snapshot was published")
			require.NotZero(t, atGrand.Load(), "no child launched at its grandparent's hook")
			require.GreaterOrEqual(t, used, uint64(6), "children started early were not used")

			// Every snapshot ends released once (discarded runs may still be
			// draining for a moment).
			require.Eventually(t, func() bool {
				refMu.Lock()
				defer refMu.Unlock()
				for _, n := range released {
					if n != 1 {
						return false
					}
				}
				return true
			}, 10*time.Second, time.Millisecond, "an early snapshot's store was not released exactly once")
			refMu.Lock()
			require.Zero(t, negative, "an early snapshot dropped more references than it held")
			require.NotEmpty(t, released)
			refMu.Unlock()
		})
	}
}

// TestLagEarlyRetryParkedReleasesOnce drives a parked offer through each
// of its outcomes on a bare pipeline and checks that the store is released
// exactly once, after both the parked slot and the run let it go:
// published at the grandparent's hook (released after the parent's hook),
// dropped by the parent's own hook arriving first, by a reset, by the run
// being superseded, and by an unsafe grandparent.
func TestLagEarlyRetryParkedReleasesOnce(t *testing.T) {
	defer SetLagEarlySnapshotForTest(true)()
	defer SetLagEarlyRetryForTest(true)()
	grandHash, parentHash := types.Hash{0x01}, types.Hash{0x02}
	setup := func(t *testing.T) (*BlockChain, *aheadExec, *int) {
		bc := &BlockChain{}
		p := &bc.lagPipe
		gp := &earlySnap{hash: grandHash, number: 1, header: &block.Header{Number: uint256.NewInt(1)}, done: make(chan struct{})}
		gp.post = state.NewEarlyPostState(gp)
		gp.refs.Store(1)
		p.early = gp
		hdr := &block.Header{Number: uint256.NewInt(2), ParentHash: grandHash}
		a := &aheadExec{blk: block.NewBlock(hdr, nil).(*block.Block), parent: grandHash, done: make(chan struct{}), pipe: p,
			es: gp, parPost: gp.post, parHeader: gp.header}
		p.next = a
		freed := new(int)
		require.True(t, bc.lagPipeEarly(a, nil, 0, nil, func() { *freed++ }), "the refused offer was not parked")
		p.mu.Lock()
		require.NotNil(t, p.parked)
		require.Nil(t, p.early.full.Load())
		p.mu.Unlock()
		require.Equal(t, uint64(1), p.earlyStats.parked.Load())
		return bc, a, freed
	}
	// runEnds is the run's goroutine ending (runAhead's deferred unref).
	runEnds := func(a *aheadExec) {
		close(a.done)
		a.pub.unref()
	}
	full := state.NewEarlyPostState(&earlySnap{done: make(chan struct{})})

	t.Run("publishedAtGrandparentHook", func(t *testing.T) {
		bc, a, freed := setup(t)
		p := &bc.lagPipe
		p.mu.Lock()
		pub := p.earlyPublishLocked(grandHash, full, map[types.Address]struct{}{})
		es := p.early
		p.mu.Unlock()
		require.True(t, pub)
		require.Same(t, a, p.spec, "the next run was not promoted")
		require.NotNil(t, es)
		require.Same(t, a.pub, es, "the published snapshot is not the parked one")
		require.Same(t, full, es.below, "the parked snapshot does not sit on the complete post-state")
		require.Nil(t, p.parked)
		require.Equal(t, uint64(1), p.earlyStats.parkedPublished.Load())
		runEnds(a)
		require.Zero(t, *freed, "released while still published")
		// The parent's own hook switches it.
		p.mu.Lock()
		require.False(t, p.earlyPublishLocked(parentHash, full, map[types.Address]struct{}{}))
		p.mu.Unlock()
		require.Equal(t, 1, *freed)
	})
	t.Run("droppedByParentHookFirst", func(t *testing.T) {
		bc, a, freed := setup(t)
		p := &bc.lagPipe
		p.mu.Lock()
		require.False(t, p.earlyPublishLocked(parentHash, full, map[types.Address]struct{}{}))
		p.mu.Unlock()
		require.Nil(t, p.parked)
		require.Equal(t, uint64(1), p.earlyStats.parkedDropped.Load())
		require.Zero(t, *freed, "released while its run still reads it")
		runEnds(a)
		require.Equal(t, 1, *freed)
	})
	t.Run("droppedByReset", func(t *testing.T) {
		bc, a, freed := setup(t)
		runEnds(a) // the run may also end first
		require.Zero(t, *freed)
		bc.lagPipeReset()
		require.Nil(t, bc.lagPipe.parked)
		require.Nil(t, bc.lagPipe.next)
		require.Equal(t, uint64(1), bc.lagPipe.earlyStats.parkedDropped.Load())
		require.Equal(t, 1, *freed)
	})
	t.Run("droppedSuperseded", func(t *testing.T) {
		bc, a, freed := setup(t)
		p := &bc.lagPipe
		other := &aheadExec{blk: a.blk, parent: grandHash, done: make(chan struct{}), pipe: p}
		close(other.done)
		p.mu.Lock()
		p.spec = other
		require.False(t, p.earlyPublishLocked(grandHash, full, map[types.Address]struct{}{}))
		p.mu.Unlock()
		require.Nil(t, p.parked)
		require.Equal(t, uint64(1), p.earlyStats.parkedDropped.Load())
		runEnds(a)
		require.Equal(t, 1, *freed)
	})
	t.Run("droppedUnsafeGrandparent", func(t *testing.T) {
		bc, a, freed := setup(t)
		p := &bc.lagPipe
		p.mu.Lock()
		require.False(t, p.earlyPublishLocked(grandHash, full, map[types.Address]struct{}{{0x99}: {}}))
		p.mu.Unlock()
		require.Same(t, a, p.spec)
		require.Nil(t, p.early)
		require.Equal(t, uint64(1), p.earlyStats.parkedDropped.Load())
		runEnds(a)
		require.Equal(t, 1, *freed)
	})
	t.Run("retryOff", func(t *testing.T) {
		defer SetLagEarlyRetryForTest(false)()
		bc := &BlockChain{}
		p := &bc.lagPipe
		gp := &earlySnap{hash: grandHash, done: make(chan struct{})}
		p.early = gp
		hdr := &block.Header{Number: uint256.NewInt(2), ParentHash: grandHash}
		a := &aheadExec{blk: block.NewBlock(hdr, nil).(*block.Block), parent: grandHash, done: make(chan struct{}), pipe: p, es: gp,
			parHeader: &block.Header{Number: uint256.NewInt(1)}}
		p.next = a
		require.False(t, bc.lagPipeEarly(a, nil, 0, nil, func() { t.Fatal("a refused store was released by the snapshot") }))
		require.Nil(t, p.parked)
		require.Nil(t, a.pub)
	})
}

// TestAheadPreEarlyCutsEarlySnapshotLaunches: with N42_AHEAD_PRE_EARLY a
// child launched on its parent's early snapshot (launchBy hook, a.es set)
// claims its preparation without waiting for a base prefill still reading;
// off, nothing is cut. Every base prefill is held until its claim, so each
// early run's claim finds its fill reading. Every block equals the serial
// chain's (the harness).
func TestAheadPreEarlyCutsEarlySnapshotLaunches(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "off"
		if on {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			defer SetAheadPreEarlyForTest(on)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetAheadBaseClaimWaitForTest(true)()
			defer r69aHoldFills()()
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
			lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if bc.lagQueuedChild(b.Hash()) != nil {
						return
					}
				}
			}
			defer func() { lagPipeAheadClaimedForTest = nil }()
			var launched uint64
			lagPipeEngineDoneForTest = func(lc *lagChain) { launched = lc.bc.lagPipe.earlyStats.launched.Load() }
			defer func() { lagPipeEngineDoneForTest = nil }()
			c0, e0, k0 := aheadPreCutClaims.Load(), aheadPreCutEarlyClaims.Load(), aheadPreCuts.Load()
			testLagPipelineAheadEngine(t, false, arrivalAny, true)
			claims, early, cuts := aheadPreCutClaims.Load()-c0, aheadPreCutEarlyClaims.Load()-e0, aheadPreCuts.Load()-k0
			t.Logf("preEarly=%v: early launches %d, cut claims %d (on an early snapshot %d), fills cut %d", on, launched, claims, early, cuts)
			require.NotZero(t, launched, "no child started on an early snapshot")
			if !on {
				require.Zero(t, claims)
				require.Zero(t, cuts)
				return
			}
			require.NotZero(t, early, "no early-snapshot launch claimed with the cut")
			require.NotZero(t, cuts, "no early-snapshot launch cut a fill still reading")
		})
	}
}

// TestLagEarlySnapshotsDoNotChain (run 66: anon RSS 1.9 -> 79 GB in two
// minutes with N42_LAG_EARLY_RETRY=1): with every block early-published,
// each block's run starts on its parent's early snapshot. Nothing may keep
// a finished snapshot's store reachable through the next one: after a
// stretch of consecutive early blocks only the last few snapshots may still
// be reachable, and every snapshot's store is released.
func TestLagEarlySnapshotsDoNotChain(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "retryOff"
		if retry {
			name = "retryOn"
		}
		t.Run(name, func(t *testing.T) {
			const blocks = 30
			defer SetLagEarlySnapshotForTest(true)()
			defer SetLagEarlyRetryForTest(retry)()
			lagPipeBlocksForTest = blocks
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() {
				lagPipeBlocksForTest = 0
				lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0
			}()
			var piped atomic.Pointer[BlockChain]
			lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
				piped.Store(bc)
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					if bc.lagQueuedChild(b.Hash()) != nil {
						return
					}
				}
			}
			defer func() { lagPipeAheadClaimedForTest = nil }()
			if retry {
				// Every offer comes before its parent's hook: parked, then
				// published there (the chain run 66 built every block).
				lagPipeBeforePublishForTest = func(h types.Hash) {
					bc := piped.Load()
					if bc == nil {
						return
					}
					p := &bc.lagPipe
					for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
						p.mu.Lock()
						a, parked := p.next, p.parked != nil && p.parkedRun == p.next
						p.mu.Unlock()
						if a == nil || a.parent != h || parked {
							return
						}
						select {
						case <-a.done:
							return
						default:
						}
					}
				}
				defer func() { lagPipeBeforePublishForTest = nil }()
			}

			// Every snapshot, weakly; and how many were freed.
			var mu sync.Mutex
			seen := map[weak.Pointer[earlySnap]]bool{}
			var freed int
			lagEarlyUnrefForTest = func(es *earlySnap, left int32) {
				mu.Lock()
				defer mu.Unlock()
				seen[weak.Make(es)] = true
				if left == 0 {
					freed++
				}
			}
			defer func() { lagEarlyUnrefForTest = nil }()
			// Near the end, while the pipeline still runs: which snapshots
			// are still reachable.
			var hooks, live, total int
			lagPipeAfterPublishForTest = func(types.Hash) {
				if hooks++; hooks != blocks-3 {
					return
				}
				runtime.GC()
				runtime.GC()
				mu.Lock()
				defer mu.Unlock()
				total = len(seen)
				for w := range seen {
					if w.Value() != nil {
						live++
					}
				}
			}
			defer func() { lagPipeAfterPublishForTest = nil }()

			testLagPipelineAheadEngine(t, false, arrivalAny, false)
			mu.Lock()
			defer mu.Unlock()
			t.Logf("snapshots seen %d at the check, reachable %d; seen in all %d, freed %d", total, live, len(seen), freed)
			require.GreaterOrEqual(t, total, blocks/2, "too few early snapshots to check a chain")
			require.LessOrEqual(t, live, 6, "finished early snapshots stay reachable: a chain of stores")
			require.GreaterOrEqual(t, freed, len(seen)-2, "early snapshots whose store was never released")
		})
	}
}
