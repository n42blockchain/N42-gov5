// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestLagPipelineExecutesAheadOfRootAndWrite (P2a2): the lag executor runs
// block N+1's Block-STM execution on N's post-state snapshot while N is
// rooted and written. A follower chain takes 12 frame blocks over the wire
// in which every sender sends in every block, so each block's nonces and
// balances are the previous block's writes: an ahead execution that did not
// see N's state would fail its transfers under the frame skip rule.
//
//   - state visibility: every block executes every transfer, and a second
//     chain executing the same blocks serially (no Block-STM, no pipeline)
//     computes the same executed result for each of them;
//   - ordering: the executor reports the imports in parent order;
//   - failure: block 5's first write fails after its snapshot was published
//     and its child started ahead; the child's ahead execution is discarded,
//     block 5 is retried, and the chain converges to the same results;
//   - the pipeline is used, and nothing is left behind: no open read
//     transaction, no available or queued block.
//
// "snapshot" makes each child finish before its parent's root touches the
// tree, so the parent's writes reach it only through the snapshot;
// "concurrent" lets the child run against the parent's root and write (and
// the evictions of that write), as in production. Two arrival orders are
// forced: "midImport" makes each child available only once its parent's
// import has passed the snapshot point (the child starts from its own
// enqueue); "beforeSnapshot" makes it available before the parent reaches
// the snapshot point, and checks that nothing ran ahead until the snapshot
// was published.
func TestLagPipelineExecutesAheadOfRootAndWrite(t *testing.T) {
	t.Run("snapshot", func(t *testing.T) { testLagPipelineAhead(t, true, arrivalAny) })
	t.Run("concurrent", func(t *testing.T) { testLagPipelineAhead(t, false, arrivalAny) })
	t.Run("midImport", func(t *testing.T) { testLagPipelineAhead(t, false, arrivalMidImport) })
	t.Run("beforeSnapshot", func(t *testing.T) { testLagPipelineAhead(t, false, arrivalBeforeSnapshot) })
	t.Run("sealed", func(t *testing.T) { testLagPipelineAhead(t, false, arrivalSealed) })
	t.Run("pushPath", func(t *testing.T) { testLagPipelineAhead(t, false, arrivalPushPath) })
}

type lagArrival int

const (
	arrivalAny lagArrival = iota
	arrivalMidImport
	arrivalBeforeSnapshot
	// sealed: the blocks are queued as a leader queues its own lag-built
	// blocks (RememberAvailable + EnqueueLagImport), holding only frame
	// hashes until their import or ahead execution resolves them (S96: a
	// child like that never ran ahead).
	arrivalSealed
	// pushPath: as N42_DEFERRED_CHECK_CONCURRENT=1 on the fleet, every
	// checked block is also handed to InsertChain by the block-push
	// goroutine, which waits on the chain lock behind the parent's import
	// and on the fleet imported most blocks itself (S96: those imports never
	// published a snapshot, and the executor's later "already stored" pass
	// discarded the child's ahead execution). Here the executor is paused
	// so that every block is the push path's.
	arrivalPushPath
)

func testLagPipelineAhead(t *testing.T, await bool, arrival lagArrival) {
	const (
		blocks   = 12
		frames   = 4
		perFrame = 100
		failAt   = 5
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	prevWait := lagPipeChildWaitForTest
	if arrival != arrivalBeforeSnapshot {
		lagPipeChildWaitForTest = 2 * time.Second
	}
	defer func() { lagPipeChildWaitForTest = prevWait }()
	lagPipeAwaitAheadForTest = await
	defer func() { lagPipeAwaitAheadForTest = false }()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	keys := frametest.Keys(t, 65, 83)
	piped := newLagChainKeys(t, keys[1:])
	piped.bc.SetParallelEVM(true)
	pipedStore := store.NewWithKeep(64<<20, 16)
	piped.bc.SetFrameStore(pipedStore)
	serial := newLagChainKeys(t, keys[1:])
	serialStore := store.NewWithKeep(64<<20, 16)
	serial.bc.SetFrameStore(serialStore)

	var orderMu sync.Mutex
	var order []uint64
	piped.bc.SetLagImportedHook(func(hash, _ types.Hash) {
		if b, err := piped.bc.GetBlockByHash(hash); err == nil && b != nil {
			orderMu.Lock()
			order = append(order, b.Number64().Uint64())
			orderMu.Unlock()
		}
	})

	if arrival == arrivalPushPath {
		lagStepPausedForTest.Store(true)
		defer lagStepPausedForTest.Store(false)
	}
	var numHash sync.Map // number -> hash of the piped chain's blocks
	waitLag := func(lc *lagChain, n uint64) {
		t.Helper()
		if lc == piped && arrival == arrivalPushPath && lagStepPausedForTest.Load() {
			deadline := time.Now().Add(60 * time.Second)
			for {
				if h, ok := numHash.Load(n); ok && lc.bc.blockStored(h.(types.Hash), n) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("push path never stored block %d", n)
				}
				time.Sleep(time.Millisecond)
			}
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			if _, last, _ := lc.bc.LagStats(); last >= n {
				return
			}
			if time.Now().After(deadline) {
				_, last, q := lc.bc.LagStats()
				t.Fatalf("lag executor at %d (queued %d), want %d", last, q, n)
			}
			time.Sleep(time.Millisecond)
		}
	}

	// beforeSnapshot: each parent's snapshot point waits for its child to
	// be available, and nothing may have started ahead on it yet.
	var early, earlyStarted int
	if arrival == arrivalBeforeSnapshot {
		lagPipeBeforePublishForTest = func(h types.Hash) {
			deadline := time.Now().Add(2 * time.Second)
			for piped.bc.lagQueuedChild(h) == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if piped.bc.lagQueuedChild(h) == nil {
				return // the last block: no child comes
			}
			piped.bc.lagPipe.mu.Lock()
			if s := piped.bc.lagPipe.spec; s != nil && s.blk.ParentHash() == h {
				earlyStarted++
			}
			piped.bc.lagPipe.mu.Unlock()
			early++
		}
		defer func() { lagPipeBeforePublishForTest = nil }()
	}
	// midImport: block n is checked only once n-1 has published its
	// snapshot (and holds there, lagPipeChildWaitForTest).
	waitPublished := func(h types.Hash) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			piped.bc.lagPipe.mu.Lock()
			par := piped.bc.lagPipe.parent
			piped.bc.lagPipe.mu.Unlock()
			if par != nil && par.hash == h {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("parent %x never published its snapshot", h[:4])
			}
			time.Sleep(100 * time.Microsecond)
		}
	}

	var pushWG sync.WaitGroup
	defer pushWG.Wait()

	fb := frametest.NewBuilder(keys[0], piped.cfg.ChainID, keys[1:])
	parent := piped.bc.genesisBlock.Header().(*block.Header)
	wire := make([]*block.Block, 0, blocks)
	for n := uint64(1); n <= blocks; n++ {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			require.True(t, pipedStore.Put(hashes[i], raws[i]))
			require.True(t, serialStore.Put(hashes[i], raws[i]))
		}
		// The leader gate: block n carries the result of n-2.
		if n >= 3 {
			waitLag(piped, n-2)
		}
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
			Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
		hdr.Number.AddUint64(hdr.Number, 1)
		if bf := misc.CalcBaseFee(piped.cfg, parent); bf != nil {
			hdr.BaseFee.SetFromBig(bf)
		}
		rtx, err := piped.db.BeginRo(piped.bc.ctx)
		require.NoError(t, err)
		res, _, err := piped.bc.LaggedExecutedResult(rtx, hdr, nil)
		rtx.Rollback()
		require.NoError(t, err)
		hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
		hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
		sealed := block.NewLaggedBlock(hdr, hashes, raws)
		w := overWire(t, sealed)
		if n == failAt {
			h := w.Hash()
			lagPipeFailWriteForTest.Store(&h)
		}
		if arrival == arrivalMidImport && n >= 2 {
			waitPublished(parent.Hash())
		}
		if arrival == arrivalSealed {
			piped.bc.RememberAvailable(sealed)
			piped.bc.EnqueueLagImport(sealed)
		} else {
			checked, retry, err := piped.bc.CheckDeferredBlock(w)
			require.True(t, checked)
			require.False(t, retry)
			require.NoError(t, err, "availability check of block %d", n)
		}
		numHash.Store(n, w.Hash())
		if arrival == arrivalPushPath {
			pushWG.Add(1)
			go func(b *block.Block, n uint64) {
				defer pushWG.Done()
				// The push goroutine imports in parent order: it waits
				// for the parent (another push goroutine's import), then
				// retries a transient failure as the future queue would.
				deadline := time.Now().Add(60 * time.Second)
				for time.Now().Before(deadline) {
					if n == 1 || piped.bc.AppliedHeadIsExactly(b.ParentHash(), n-1) {
						if _, err := piped.bc.InsertChain([]block.IBlock{b}); err == nil && piped.bc.blockStored(b.Hash(), n) {
							return
						}
					}
					time.Sleep(time.Millisecond)
				}
			}(w, n)
		}
		wire = append(wire, w)
		parent = w.Header().(*block.Header)
	}
	waitLag(piped, blocks)
	pushWG.Wait()
	require.Nil(t, lagPipeFailWriteForTest.Load(), "the injected write failure never fired")
	if arrival == arrivalPushPath {
		// The executor drains the blocks the push path stored.
		lagStepPausedForTest.Store(false)
		require.Eventually(t, func() bool {
			_, _, q := piped.bc.LagStats()
			return q == 0
		}, 10*time.Second, time.Millisecond, "the executor kept blocks the push path stored")
	}

	// The serial reference executes the same wire blocks.
	for _, w := range wire {
		checked, retry, err := serial.bc.CheckDeferredBlock(overWire(t, w))
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "serial availability check of block %d", w.Number64().Uint64())
	}
	waitLag(serial, blocks)

	for _, w := range wire {
		n := w.Number64().Uint64()
		got, want := piped.result(w.Hash()), serial.result(w.Hash())
		require.Equal(t, uint64(frames*perFrame*21000), got.GasUsed, "block %d did not execute every transfer", n)
		require.Equal(t, want, got, "block %d: pipelined result differs from the serial one", n)
	}

	orderMu.Lock()
	if arrival == arrivalPushPath {
		// The executor reports only its own imports; the rest were the
		// push goroutines'.
		require.Empty(t, order, "the paused executor imported blocks")
	} else {
		require.Len(t, order, blocks)
		for i, n := range order {
			require.Equal(t, uint64(i+1), n, "imports out of parent order: %v", order)
		}
	}
	orderMu.Unlock()

	launched, used, discarded, _ := piped.bc.LagPipelineStats()
	skips := piped.bc.LagPipelineSkipStats()
	t.Logf("ahead executions: launched %d, used %d, discarded %d, skips %+v", launched, used, discarded, skips)
	require.GreaterOrEqual(t, used, uint64(blocks-4), "the pipeline barely ran")
	require.Zero(t, skips.Disabled)
	switch arrival {
	case arrivalMidImport:
		// Every child but the failed parent's first one is used; only the
		// last block has no child.
		require.GreaterOrEqual(t, used, uint64(blocks-1), "a child available mid-import did not run ahead")
		require.LessOrEqual(t, skips.NotAvailable, uint64(1))
	case arrivalBeforeSnapshot:
		require.GreaterOrEqual(t, early, blocks-1, "children did not become available before the snapshot")
		require.Zero(t, earlyStarted, "a child ran ahead before its parent's snapshot")
		require.GreaterOrEqual(t, used, uint64(blocks-1), "a child available before the snapshot did not run ahead")
	}
	require.GreaterOrEqual(t, discarded, uint64(1), "the failed parent's child was not discarded")
	require.Eventually(t, func() bool {
		_, _, _, open := piped.bc.LagPipelineStats()
		return open == 0
	}, 5*time.Second, time.Millisecond, "ahead executions left read transactions open")
	piped.bc.lagPipe.mu.Lock()
	require.Nil(t, piped.bc.lagPipe.spec)
	require.Nil(t, piped.bc.lagPipe.parent)
	piped.bc.lagPipe.mu.Unlock()
	_, _, queued := piped.bc.LagStats()
	require.Zero(t, queued)
	piped.bc.avail.mu.Lock()
	require.Empty(t, piped.bc.avail.m)
	piped.bc.avail.mu.Unlock()
}

// TestLagPipelineOrphanedChildDiscardsAhead (P2a2): a child whose ahead
// execution has started on its parent's snapshot is orphaned (forgotten:
// the losing-fork / invalid path) while that execution runs. The ahead
// execution is discarded and releases its read transaction, the orphan is
// never imported, its sibling imports on the serial path with every
// transfer, and the pipeline runs ahead again for the next block.
func TestLagPipelineOrphanedChildDiscardsAhead(t *testing.T) {
	const (
		frames   = 4
		perFrame = 100
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	prevWait := lagPipeChildWaitForTest
	lagPipeChildWaitForTest = 2 * time.Second
	defer func() { lagPipeChildWaitForTest = prevWait }()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	keys := frametest.Keys(t, 65, 91)
	lc := newLagChainKeys(t, keys[1:])
	lc.bc.SetParallelEVM(true)
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])

	waitLag := func(n uint64) {
		t.Helper()
		require.Eventually(t, func() bool {
			_, last, _ := lc.bc.LagStats()
			return last >= n
		}, 60*time.Second, time.Millisecond, "lag executor never reached %d", n)
	}
	type frameSet struct {
		raws   [][]byte
		hashes []types.Hash
	}
	newFrames := func() frameSet {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]))
		}
		return frameSet{raws, hashes}
	}
	build := func(parent *block.Header, fs frameSet, dt uint64) *block.Block {
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + dt, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
			Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
		hdr.Number.AddUint64(hdr.Number, 1)
		if bf := misc.CalcBaseFee(lc.cfg, parent); bf != nil {
			hdr.BaseFee.SetFromBig(bf)
		}
		rtx, err := lc.db.BeginRo(lc.bc.ctx)
		require.NoError(t, err)
		res, _, err := lc.bc.LaggedExecutedResult(rtx, hdr, nil)
		rtx.Rollback()
		require.NoError(t, err)
		hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
		hdr.SetFrameTxRoot(block.FrameTxRoot(fs.hashes))
		return overWire(t, block.NewLaggedBlock(hdr, fs.hashes, fs.raws))
	}
	check := func(b *block.Block) {
		t.Helper()
		checked, retry, err := lc.bc.CheckDeferredBlock(b)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", b.Number64().Uint64())
	}

	genesis := lc.bc.genesisBlock.Header().(*block.Header)
	b1 := build(genesis, newFrames(), 10)
	check(b1)
	b2 := build(b1.Header().(*block.Header), newFrames(), 10)
	check(b2)
	waitLag(1)

	// The orphan: block 3 on b2. Its ahead execution is orphaned as soon as
	// it holds its read transaction, while b2 is still at its snapshot.
	f3 := newFrames()
	orphan := build(b2.Header().(*block.Header), f3, 10)
	var orphaned atomic.Bool
	lagPipeAheadStartedForTest = func(child *block.Block) {
		if child.Hash() == orphan.Hash() && orphaned.CompareAndSwap(false, true) {
			lc.bc.lagForget(child.Hash())
		}
	}
	defer func() { lagPipeAheadStartedForTest = nil }()
	_, _, discardedBefore, _ := lc.bc.LagPipelineStats()
	check(orphan)
	waitLag(2)
	require.True(t, orphaned.Load(), "the orphan's ahead execution never started")
	_, used, discarded, _ := lc.bc.LagPipelineStats()
	require.Greater(t, discarded, discardedBefore, "the orphan's ahead execution was not discarded")
	require.Eventually(t, func() bool {
		_, _, _, open := lc.bc.LagPipelineStats()
		return open == 0
	}, 5*time.Second, time.Millisecond, "the orphan's ahead execution kept its read transaction")
	require.Nil(t, lc.bc.AvailableBlock(orphan.Hash()), "the orphan stayed available")
	require.False(t, lc.bc.LagImportQueued(orphan.Hash()), "the orphan stayed queued")

	// Its sibling (same transactions, another header) wins block 3; block
	// 4 runs ahead on the sibling's snapshot.
	sibling := build(b2.Header().(*block.Header), f3, 11)
	require.NotEqual(t, orphan.Hash(), sibling.Hash())
	check(sibling)
	waitLag(2)
	b4 := build(sibling.Header().(*block.Header), newFrames(), 10)
	check(b4)
	waitLag(4)
	require.False(t, lc.bc.blockStored(orphan.Hash(), 3), "the orphan was imported")
	for _, b := range []*block.Block{b1, b2, sibling, b4} {
		require.Equal(t, uint64(frames*perFrame*21000), lc.result(b.Hash()).GasUsed, "block %d did not execute every transfer", b.Number64().Uint64())
	}
	_, usedAfter, _, _ := lc.bc.LagPipelineStats()
	require.Greater(t, usedAfter, used, "the pipeline did not run ahead again after the orphan")
	require.Eventually(t, func() bool {
		_, _, _, open := lc.bc.LagPipelineStats()
		return open == 0
	}, 5*time.Second, time.Millisecond, "ahead executions left read transactions open")
	lc.bc.lagPipe.mu.Lock()
	require.Nil(t, lc.bc.lagPipe.spec)
	require.Nil(t, lc.bc.lagPipe.parent)
	lc.bc.lagPipe.mu.Unlock()
	_, _, queued := lc.bc.LagStats()
	require.Zero(t, queued)
}
