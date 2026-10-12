// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
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

// TestFrameSenderRecoveredOncePerNode (P8a): a follower admits frames,
// recovers their senders at admission, and imports blocks of those frames
// through the availability check, the per-block sender job and the lag
// executor. Every transaction's sender is recovered exactly once on the
// node, whichever path reaches its frame first: admission before the
// block, the block before admission, or both at once.
func TestFrameSenderRecoveredOncePerNode(t *testing.T) {
	// Fresh keys per case: the process-wide sender cache would otherwise
	// answer for the previous case's identical transactions.
	salt := byte(90)
	for _, tc := range []struct {
		name     string
		admitAll bool // admission finishes before the block arrives
		workers  int  // admission workers
		parallel bool // Block-STM import
	}{
		{"admission-first", true, 4, false},
		{"race", false, 4, false},
		{"slow-admission", false, 1, false},
		{"race-parallel", false, 4, true},
	} {
		salt++
		t.Run(tc.name, func(t *testing.T) {
			testFrameSenderRecoveredOnce(t, salt, tc.admitAll, tc.workers, tc.parallel)
		})
	}
}

func testFrameSenderRecoveredOnce(t *testing.T, salt byte, admitAll bool, workers int, parallel bool) {
	const (
		blocks   = 4
		frames   = 12
		perFrame = 200
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	keys := frametest.Keys(t, 41, salt)
	lc := newLagChainKeys(t, keys[1:])
	if parallel {
		lc.bc.SetParallelEVM(true)
	}
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	signer := transaction.LatestSignerForChainID(lc.cfg.ChainID)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, workers)
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

	// Sign everything first: signing must not be counted, and the frames
	// enter the store block by block below.
	type blockFrames struct {
		raws   [][]byte
		hashes []types.Hash
	}
	all := make([]blockFrames, blocks)
	for i := range all {
		all[i].raws, all[i].hashes, _ = fb.Frames(t, frames, perFrame)
	}
	stop := transaction.CountSenderRecoveriesForTest()
	defer stop()

	parent := lc.bc.genesisBlock.Header().(*block.Header)
	for n := uint64(1); n <= blocks; n++ {
		raws, hashes := all[n-1].raws, all[n-1].hashes
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]))
		}
		if admitAll {
			for rec.Pending() > 0 {
				time.Sleep(time.Millisecond)
			}
		}
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
	}
	waitLag(blocks)
	// Admission may still be draining frames a block already finished.
	for rec.Pending() > 0 {
		time.Sleep(time.Millisecond)
	}
	got := stop()
	want := uint64(blocks * frames * perFrame)
	require.Equal(t, uint64(frames*perFrame*21000), lc.result(parent.Hash()).GasUsed, "the last block did not execute every transfer")
	t.Logf("sender recoveries %d for %d transactions (%.3f per tx); admission %+v", got, want, float64(got)/float64(want), rec.Stats())
	require.Equal(t, want, got, "every transaction's sender must be recovered exactly once")
	if admitAll {
		// P10: every frame's senders were cached before its block was
		// resolved, so each import found them complete and walked nothing.
		require.Equal(t, uint64(blocks), lc.bc.SendersDoneHits(), "imports that found their senders complete")
	}
}
