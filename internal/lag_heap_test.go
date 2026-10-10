// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// lagChainGasLimit is the genesis gas limit of a lagChain.
var lagChainGasLimit uint64 = 30_000_000

// TestLagPipelineHeapBounded reproduces S93/S94: under P2a (frame blocks,
// availability vote, depth 2) every node's live heap grew by 0.2-0.35 GB
// per imported block until the box thrashed. A follower chain here takes 40
// frame blocks of 20,000 transfers each over the wire, checks availability,
// and imports them through the lag executor, with sender recovery at
// admission and a frame store of fixed budget and keep margin. After the
// warm-up (the block cache and the frame store full), the live heap after a
// GC must not grow by more than a small fraction of one block per block:
// nothing but state legitimately accumulates, and the state here is a
// fixed set of accounts.
func TestLagPipelineHeapBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("signs and imports 800,000 transactions")
	}
	const (
		blocks    = 40
		frames    = 40
		perFrame  = 500
		warmup    = 20
		boundPerB = 4 << 20 // bytes of live heap per block after the warm-up
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	// The fleet's N42_BLOCK_CACHE_BLOCKS=16.
	prevCache := blockCacheLimit
	blockCacheLimit = 16
	defer func() { blockCacheLimit = prevCache }()

	keys := frametest.Keys(t, 65, 71)
	lc := newLagChainKeys(t, keys[1:])
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	signer := transaction.LatestSignerForChainID(lc.cfg.ChainID)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 8)
	rec.Start(context.Background())
	defer rec.Stop()
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])

	waitLag := func(n uint64) {
		deadline := time.Now().Add(60 * time.Second)
		for {
			if _, last, _ := lc.bc.LagStats(); last >= n {
				return
			}
			if time.Now().After(deadline) {
				_, last, q := lc.bc.LagStats()
				t.Fatalf("lag executor at %d (queued %d), want %d", last, q, n)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	heapAfterGC := func() uint64 {
		runtime.GC()
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse
	}

	parent := lc.bc.genesisBlock.Header().(*block.Header)
	inuse := make([]uint64, 0, blocks+1)
	inuse = append(inuse, heapAfterGC())
	for n := uint64(1); n <= blocks; n++ {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]), "frame store refused frame %d of block %d", i, n)
		}
		for rec.Pending() > 0 {
			time.Sleep(time.Millisecond)
		}
		// The leader gate: block n carries the result of n-2.
		if n >= 2 {
			waitLag(n - 2)
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
		w := overWire(t, block.NewLaggedBlock(hdr, hashes, raws))
		checked, retry, err := lc.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", n)
		parent = w.Header().(*block.Header)
		waitLag(n)
		inuse = append(inuse, heapAfterGC())
		lc.bc.avail.mu.Lock()
		nAvail := len(lc.bc.avail.m)
		lc.bc.avail.mu.Unlock()
		require.LessOrEqual(t, nAvail, 3, "imported blocks stay in the available-block cache")
		t.Logf("block %2d: HeapInuse %7.1f MiB (%+7.1f MiB) avail %d blockCache %d receipts %d future %d headers %d witness %d store %d/%d MiB",
			n, float64(inuse[n])/(1<<20), (float64(inuse[n])-float64(inuse[n-1]))/(1<<20), nAvail,
			lc.bc.blockCache.Len(), lc.bc.receiptCache.Len(), lc.bc.futureBlocks.Len(), lc.bc.headerCache.Len(),
			lc.bc.witnessCache.Len(), st.Len(), st.Stats().Bytes>>20)
	}
	require.Equal(t, uint64(frames*perFrame*21000), lc.result(parent.Hash()).GasUsed, "block %d did not execute every transfer", blocks)
	perBlock := (int64(inuse[blocks]) - int64(inuse[warmup])) / int64(blocks-warmup)
	t.Logf("live heap growth after block %d: %.2f MiB per block (bound %.2f MiB)", warmup,
		float64(perBlock)/(1<<20), float64(boundPerB)/(1<<20))
	if perBlock > boundPerB {
		dir := os.Getenv("N42_HEAP_PROFILE_DIR")
		if dir == "" {
			dir = t.TempDir()
		}
		path := filepath.Join(dir, "lag_pipeline_heap.pprof")
		if f, err := os.Create(path); err == nil {
			_ = pprof.WriteHeapProfile(f)
			_ = f.Close()
			t.Logf("heap profile: %s", path)
		}
		t.Fatalf("live heap grows %s per block under the lag pipeline (bound %s)",
			fmt.Sprintf("%.2f MiB", float64(perBlock)/(1<<20)), fmt.Sprintf("%.2f MiB", float64(boundPerB)/(1<<20)))
	}
}
