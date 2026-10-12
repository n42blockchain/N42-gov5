// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// buildFrameBlocksOn builds n frame blocks of two frames on lc and imports each
// one alone through the availability check and the lag executor (the
// ordered writer), digesting the tables after each write.
func buildFrameBlocksOn(t *testing.T, lc *lagChain, fb *frametest.Builder, n int) (blks []*block.Block, dumps []map[string]string) {
	t.Helper()
	st := store.New(1 << 26)
	lc.bc.SetFrameStore(st)
	imported := 0
	importNext := func() {
		w := overWire(t, blks[imported])
		checked, retry, err := lc.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", imported+1)
		imported++
		waitLagWritten(t, lc.bc, uint64(imported))
		lc.bc.lock.Lock()
		require.NoError(t, lc.bc.joinLagWrite())
		lc.bc.lock.Unlock()
		dumps = append(dumps, dumpTables(t, lc.db))
	}
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	for i := 0; i < n; i++ {
		raws, hashes, _ := fb.Frames(t, 2, 3)
		for j := range raws {
			require.True(t, st.Put(hashes[j], raws[j]))
		}
		b := lagBuild(t, lc, parent, hashes, raws)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
		if i >= 1 {
			importNext()
		}
	}
	importNext()
	return blks, dumps
}

// withoutFrames drops the Frames table from a digest.
func withoutFrames(d map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range d {
		if k != modules.Frames {
			out[k] = v
		}
	}
	return out
}

// TestLagWriterFrameStoreSameRows (P11, N42_FRAME_DB): with a frame store
// registered, the ordered writer writes each block's frames there, in their
// own transaction, and the chaindata holds, block by block, exactly the rows
// of a chain without one except the Frames table, which stays empty; the
// frame store holds exactly the reference's Frames rows, and frames and
// frame-body transactions read back the same through rawdb.
func TestLagWriterFrameStoreSameRows(t *testing.T) {
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	keys := frametest.Keys(t, 4, 83)
	const n = 6

	ref := newLagChainEngine(t, keys[1:], importBenchEngine{})
	blks, want := buildFrameBlocksOn(t, ref, frametest.NewBuilder(keys[0], ref.cfg.ChainID, keys[1:]), n)
	require.NotEmpty(t, want[n-1][modules.Frames])

	fdb := memdb.New(t.TempDir())
	defer fdb.Close()
	defer rawdb.SetFrameStore(fdb)()

	var frameWrites int
	blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
		if ph.Frames > 0 {
			frameWrites++
		}
	}
	defer func() { blockWritePhasesHook = nil }()

	got := newLagChainEngine(t, keys[1:], importBenchEngine{})
	st := store.New(1 << 26)
	got.bc.SetFrameStore(st)
	for i, b := range blks {
		body := b.Body().(*block.Body)
		for j, h := range body.FrameHashes {
			require.True(t, st.Put(h, body.FrameData[j]))
		}
		w := overWire(t, b)
		checked, retry, err := got.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err)
		waitLagWritten(t, got.bc, uint64(i+1))
		got.bc.lock.Lock()
		require.NoError(t, got.bc.joinLagWrite())
		got.bc.lock.Unlock()
		d := dumpTables(t, got.db)
		require.Empty(t, d[modules.Frames], "after block %d: frames in the chaindata", i+1)
		requireSameTables(t, withoutFrames(want[i]), d, "after block "+itoa(i+1))
		require.Equal(t, want[i][modules.Frames], dumpTables(t, fdb)[modules.Frames], "after block %d: frame store", i+1)
		require.Equal(t, ref.result(b.Hash()), got.result(b.Hash()))
	}
	require.Equal(t, n, frameWrites, "writes that sent their frames to the frame store")

	// Reads: every frame and every frame body's transactions.
	require.NoError(t, got.db.View(context.Background(), func(tx kv.Tx) error {
		for _, b := range blks {
			body := b.Body().(*block.Body)
			for j, h := range body.FrameHashes {
				raw, err := rawdb.ReadFrame(tx, h)
				require.NoError(t, err)
				require.Equal(t, body.FrameData[j], raw)
				ok, err := rawdb.HasFrame(tx, h)
				require.NoError(t, err)
				require.True(t, ok)
			}
			hashes, err := rawdb.ReadFrameBodyHashes(tx, b.Hash(), b.Number64().Uint64())
			require.NoError(t, err)
			require.Equal(t, body.FrameHashes, hashes)
			txs, err := rawdb.ReadFrameTxs(tx, hashes)
			require.NoError(t, err)
			wantTxs, _, err := frame.DecodeFrames(body.FrameData, 0)
			require.NoError(t, err)
			require.Len(t, txs, len(wantTxs))
			require.NotEmpty(t, txs)
			for k := range txs {
				require.Equal(t, wantTxs[k].Hash(), txs[k].Hash())
			}
		}
		ok, err := rawdb.HasFrame(tx, types.Hash{1})
		require.NoError(t, err)
		require.False(t, ok)
		return nil
	}))
}

// TestFrameStoreSyncWriteAndFallback: a block written outside the ordered
// writer (WriteBlock) also sends its frames to the frame store, and frames
// stored in the chaindata Frames table before the frame store existed stay
// readable.
func TestFrameStoreSyncWriteAndFallback(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	keys := frametest.Keys(t, 2, 89)
	cfgID := newLagChainEngine(t, keys[1:], importBenchEngine{}).cfg.ChainID
	fb := frametest.NewBuilder(keys[0], cfgID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 3, 2)

	db := memdb.New(t.TempDir())
	defer db.Close()
	ctx := context.Background()
	// An old frame, in the Frames table.
	require.NoError(t, db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteFrame(tx, hashes[0], raws[0]) }))

	fdb := memdb.New(t.TempDir())
	defer fdb.Close()
	defer rawdb.SetFrameStore(fdb)()

	hdr := &block.Header{Number: uint256.NewInt(1), GasLimit: 1, Difficulty: uint256.NewInt(1), BaseFee: uint256.NewInt(1)}
	hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
	// Frame 0 travels without data (already stored), 1 and 2 with it.
	data := [][]byte{nil, raws[1], raws[2]}
	b := block.NewLaggedBlock(hdr, hashes, data)
	require.NoError(t, db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteBlock(tx, b) }))

	require.NoError(t, db.View(ctx, func(tx kv.Tx) error {
		for i, h := range hashes {
			raw, err := rawdb.ReadFrame(tx, h)
			require.NoError(t, err)
			require.Equal(t, raws[i], raw, "frame %d", i)
		}
		// Only the old frame is in the chaindata table.
		n := 0
		require.NoError(t, tx.ForEach(modules.Frames, nil, func(k, v []byte) error { n++; return nil }))
		require.Equal(t, 1, n)
		return nil
	}))
	require.NoError(t, fdb.View(ctx, func(tx kv.Tx) error {
		n := 0
		require.NoError(t, tx.ForEach(modules.Frames, nil, func(k, v []byte) error { n++; return nil }))
		require.Equal(t, 2, n, "frames with data in the frame store")
		return nil
	}))
}
