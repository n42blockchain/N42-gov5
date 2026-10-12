// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestLagGateOpensOnAppliedResult (P12): under deferred depth 2 the leader
// gate for height 3 needs block 1's executed result. Block 1's write is
// held in the ordered writer; with N42_LAG_GATE_APPLIED the gate opens, the
// build's result lookup answers and the result hook fires while the write
// is still in flight, and the result equals what the write then stores.
// With the switch off the gate stays shut until the write is durable.
func TestLagGateOpensOnAppliedResult(t *testing.T) {
	t.Run("applied", func(t *testing.T) { testLagGateApplied(t, true) })
	t.Run("durable", func(t *testing.T) { testLagGateApplied(t, false) })
}

func testLagGateApplied(t *testing.T, applied bool) {
	const (
		frames   = 2
		perFrame = 50
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagGateAppliedForTest(applied)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	release := make(chan struct{})
	lagWriteBeforeCommitForTest = func(n uint64) error {
		if n == 1 {
			<-release
		}
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	keys := frametest.Keys(t, 65, 131)
	lc := newLagChainEngine(t, keys[1:], importBenchEngine{})
	lc.bc.SetParallelEVM(true)
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])
	resultReady := make(chan types.Hash, 4)
	lc.bc.SetLagResultHook(func(h types.Hash) { resultReady <- h })

	build := func(parent *block.Header) *block.Block {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]))
		}
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
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
		hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
		b := overWire(t, block.NewLaggedBlock(hdr, hashes, raws))
		checked, retry, err := lc.bc.CheckDeferredBlock(b)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err)
		return b
	}
	b1 := build(lc.bc.genesisBlock.Header().(*block.Header))
	b2 := build(b1.Header().(*block.Header))
	h1 := b1.Header().(*block.Header)

	// Block 1 is applied and handed off; its write waits for release.
	require.Eventually(t, func() bool { return lc.bc.lagWritePending(b1.Hash()) != nil }, 30*time.Second, time.Millisecond)

	storeResult := func() (rawdb.ExecutedResult, bool) {
		var r rawdb.ExecutedResult
		var ok bool
		require.NoError(t, lc.db.View(lc.bc.ctx, func(tx kv.Tx) error {
			var err error
			r, ok, err = rawdb.ReadExecutedResult(tx, b1.Hash())
			return err
		}))
		return r, ok
	}
	if applied {
		require.Eventually(t, func() bool {
			ready, _, err := lc.bc.LagReady(b2.Hash())
			require.NoError(t, err)
			return ready
		}, 30*time.Second, time.Millisecond, "the gate did not open on block 1's applied result")
		require.NotNil(t, lc.bc.lagWritePending(b1.Hash()), "block 1's write landed before the check")
		_, durable := storeResult()
		require.False(t, durable)
		select {
		case h := <-resultReady:
			require.Equal(t, b1.Hash(), h)
		case <-time.After(10 * time.Second):
			t.Fatal("the result hook never fired")
		}
		rtx, err := lc.db.BeginRo(lc.bc.ctx)
		require.NoError(t, err)
		pending, err := lc.bc.ExecutedOrSeededResult(rtx, h1)
		rtx.Rollback()
		require.NoError(t, err)
		close(release)
		released = true
		require.Eventually(t, func() bool { _, ok := storeResult(); return ok }, 30*time.Second, time.Millisecond)
		stored, _ := storeResult()
		require.Equal(t, stored, pending, "the in-memory result differs from the stored one")
	} else {
		time.Sleep(200 * time.Millisecond)
		ready, waitFor, err := lc.bc.LagReady(b2.Hash())
		require.NoError(t, err)
		require.False(t, ready, "the gate opened before block 1 was durable")
		require.Equal(t, b1.Hash(), waitFor)
		require.Empty(t, resultReady)
		close(release)
		released = true
		require.Eventually(t, func() bool {
			ready, _, err := lc.bc.LagReady(b2.Hash())
			require.NoError(t, err)
			return ready
		}, 30*time.Second, time.Millisecond)
	}
	require.Eventually(t, func() bool { _, last, _ := lc.bc.LagStats(); return last >= 2 }, 30*time.Second, time.Millisecond)
}

// TestLagCycleLeaderGateModel (P12) models the fleet's import cycle at
// deferred depth 2 with the stage durations scaled down (S103 node0 B1:
// ahead run ~120 ms, root ~58, the ordered writer's write ~137, proposal to
// queued ~50): the ahead run, the root and the write are stretched by test
// hooks to 60, 30 and 70 ms, and a leader proposes block n once LagReady
// opens for its parent, the block arriving 25 ms later. With the gate on
// the durable write the proposal of n+1 waits for n-1's write, so n+1
// arrives after n's import (the cycle is (write+arrival+run+root)/2, ~92
// ms here); on the applied result it arrives while n runs, the ahead run is
// used on every block and the cycle falls to the write (~70 ms).
func TestLagCycleLeaderGateModel(t *testing.T) {
	cycle := map[bool]time.Duration{}
	for _, applied := range []bool{false, true} {
		name := map[bool]string{false: "durable", true: "applied"}[applied]
		t.Run(name, func(t *testing.T) { cycle[applied] = testLagCycleModel(t, applied) })
	}
	if cycle[false] > 0 && cycle[true] > 0 {
		t.Logf("cycle per block: durable gate %v, applied gate %v", cycle[false], cycle[true])
		require.Less(t, float64(cycle[true]), 0.9*float64(cycle[false]), "the applied gate did not shorten the cycle")
	}
}

func testLagCycleModel(t *testing.T, applied bool) time.Duration {
	const (
		blocks   = 18
		frames   = 2
		perFrame = 40
		runDelay = 60 * time.Millisecond
		rootLag  = 30 * time.Millisecond
		write    = 70 * time.Millisecond
		arrival  = 25 * time.Millisecond
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagGateAppliedForTest(applied)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()
	lagWriteBeforeCommitForTest = func(uint64) error { time.Sleep(write); return nil }
	lagPipeAheadStartedForTest = func(*block.Block) { time.Sleep(runDelay) }
	lagPipeAfterPublishForTest = func(types.Hash) { time.Sleep(rootLag) }
	defer func() {
		lagWriteBeforeCommitForTest, lagPipeAheadStartedForTest, lagPipeAfterPublishForTest = nil, nil, nil
	}()

	keys := frametest.Keys(t, 65, 132)
	lc := newLagChainEngine(t, keys[1:], importBenchEngine{})
	lc.bc.SetParallelEVM(true)
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])

	var endMu sync.Mutex
	ends := map[types.Hash]time.Time{}
	lc.bc.SetLagImportedHook(func(hash, _ types.Hash) {
		endMu.Lock()
		ends[hash] = time.Now()
		endMu.Unlock()
	})
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	var hashes []types.Hash
	for n := uint64(1); n <= blocks; n++ {
		raws, fh, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			require.True(t, st.Put(fh[i], raws[i]))
		}
		// The leader: block n once the gate opens on its parent.
		if n >= 3 {
			require.Eventually(t, func() bool {
				ready, _, err := lc.bc.LagReady(parent.Hash())
				require.NoError(t, err)
				return ready
			}, 30*time.Second, 100*time.Microsecond)
		}
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
			Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
		hdr.Number.AddUint64(hdr.Number, 1)
		if bf := misc.CalcBaseFee(lc.cfg, parent); bf != nil {
			hdr.BaseFee.SetFromBig(bf)
		}
		rtx, err := lc.db.BeginRo(lc.bc.ctx)
		require.NoError(t, err)
		target, err := lc.bc.LaggedTarget(rtx, hdr, nil)
		require.NoError(t, err)
		res, err := lc.bc.ExecutedOrSeededResult(rtx, target)
		rtx.Rollback()
		require.NoError(t, err)
		hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
		hdr.SetFrameTxRoot(block.FrameTxRoot(fh))
		b := overWire(t, block.NewLaggedBlock(hdr, fh, raws))
		// Proposal to queued (push, availability check).
		time.Sleep(arrival)
		checked, retry, err := lc.bc.CheckDeferredBlock(b)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err)
		hashes = append(hashes, b.Hash())
		parent = b.Header().(*block.Header)
	}
	require.Eventually(t, func() bool { _, last, _ := lc.bc.LagStats(); return last >= blocks }, 60*time.Second, time.Millisecond)
	for i, h := range hashes {
		require.Equal(t, uint64(frames*perFrame*21000), lc.result(h).GasUsed, "block %d did not execute every transfer", i+1)
	}
	// The cycle: written blocks per unit of time, past the start-up.
	endMu.Lock()
	from, to := ends[hashes[3]], ends[hashes[blocks-1]]
	endMu.Unlock()
	require.False(t, from.IsZero() || to.IsZero())
	cycle := to.Sub(from) / time.Duration(blocks-1-3)
	launched, used, discarded, _ := lc.bc.LagPipelineStats()
	p := &lc.bc.lagPipe
	t.Logf("cycle %v; ahead launched %d used %d discarded %d late %d takeLaunch %d", cycle, launched, used, discarded, p.late.Load(), p.takeLaunch.Load())
	require.GreaterOrEqual(t, used, uint64(blocks-1))
	return cycle
}
