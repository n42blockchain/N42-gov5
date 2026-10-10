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
func TestLagPipelineExecutesAheadOfRootAndWrite(t *testing.T) {
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
	lagPipeChildWaitForTest = 2 * time.Second
	defer func() { lagPipeChildWaitForTest = prevWait }()
	// The child executes before the parent's root touches the tree: it sees
	// the parent's writes through the snapshot or not at all.
	lagPipeAwaitAheadForTest = true
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

	waitLag := func(lc *lagChain, n uint64) {
		t.Helper()
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
		w := overWire(t, block.NewLaggedBlock(hdr, hashes, raws))
		if n == failAt {
			h := w.Hash()
			lagPipeFailWriteForTest.Store(&h)
		}
		checked, retry, err := piped.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", n)
		wire = append(wire, w)
		parent = w.Header().(*block.Header)
	}
	waitLag(piped, blocks)
	require.Nil(t, lagPipeFailWriteForTest.Load(), "the injected write failure never fired")

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
	require.Len(t, order, blocks)
	for i, n := range order {
		require.Equal(t, uint64(i+1), n, "imports out of parent order: %v", order)
	}
	orderMu.Unlock()

	launched, used, discarded, _ := piped.bc.LagPipelineStats()
	t.Logf("ahead executions: launched %d, used %d, discarded %d", launched, used, discarded)
	require.GreaterOrEqual(t, used, uint64(blocks-4), "the pipeline barely ran")
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
