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
	testLagWriterFrameStoreSameRows(t, false)
}

// TestLagWriterFrameStoreSameRowsEarly is TestLagWriterFrameStoreSameRows
// with N42_FRAMES_WRITE_EARLY: the frames are queued for the frame store
// when the availability check queues the block, the hand-off takes that
// write, and the chaindata and the frame store end up with the same rows.
func TestLagWriterFrameStoreSameRowsEarly(t *testing.T) {
	testLagWriterFrameStoreSameRows(t, true)
}

func testLagWriterFrameStoreSameRows(t *testing.T, early bool) {
	defer SetFramesWriteEarlyForTest(early)()
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
	got.bc.framesQ.mu.Lock()
	require.Empty(t, got.bc.framesQ.byHash, "early frames writes not taken by a hand-off")
	if early {
		require.Equal(t, uint64(n), got.bc.framesQ.taken, "hand-offs that took the early frames write")
	} else {
		require.Zero(t, got.bc.framesQ.taken)
	}
	got.bc.framesQ.mu.Unlock()

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

// TestFramesWriteSerialBatch (N42_FRAMES_WRITE_EARLY): frames writes queued
// while the serial writer is busy go to the frame store in one transaction,
// each completing only once it committed; a dropped early write is
// forgotten but still lands.
func TestFramesWriteSerialBatch(t *testing.T) {
	defer SetFramesWriteEarlyForTest(true)()
	fdb := memdb.New(t.TempDir())
	defer fdb.Close()
	defer rawdb.SetFrameStore(fdb)()

	bc := &BlockChain{}
	var writes []*framesWrite
	want := map[types.Hash][]byte{}
	for i := 0; i < 3; i++ {
		var hashes []types.Hash
		var raws [][]byte
		for j := 0; j < 2; j++ {
			h := types.Hash{byte(i + 1), byte(j + 1)}
			raw := []byte{byte(i), byte(j), 42}
			hashes, raws = append(hashes, h), append(raws, raw)
			want[h] = raw
		}
		writes = append(writes, &framesWrite{done: make(chan struct{}), hashes: hashes, raws: raws})
	}
	// Queued while the writer runs: picked up as one batch.
	bc.framesQ.pending = append(bc.framesQ.pending, writes...)
	bc.framesQ.running = true
	bc.framesWriteLoop()
	for i, fw := range writes {
		require.NoError(t, fw.wait(), "write %d", i)
		require.Equal(t, writes[0].took, fw.took, "write %d not in the first batch", i)
	}
	require.False(t, bc.framesQ.running)
	require.NoError(t, fdb.View(context.Background(), func(tx kv.Tx) error {
		n := 0
		require.NoError(t, tx.ForEach(modules.Frames, nil, func(k, v []byte) error {
			n++
			require.Equal(t, want[types.BytesToHash(k)], v)
			return nil
		}))
		require.Equal(t, len(want), n)
		return nil
	}))

	// A block queued early and dropped before any hand-off: its frames
	// still land, and nothing names its write any more.
	keys := frametest.Keys(t, 2, 97)
	cfgID := newLagChainEngine(t, keys[1:], importBenchEngine{}).cfg.ChainID
	fb := frametest.NewBuilder(keys[0], cfgID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 2, 2)
	hdr := &block.Header{Number: uint256.NewInt(1), GasLimit: 1, Difficulty: uint256.NewInt(1), BaseFee: uint256.NewInt(1)}
	hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
	b := block.NewLaggedBlock(hdr, hashes, raws)
	bc.startFramesWriteEarly(b)
	bc.framesQ.mu.Lock()
	fw := bc.framesQ.byHash[b.Hash()]
	bc.framesQ.mu.Unlock()
	require.NotNil(t, fw)
	bc.dropFramesWriteEarly(b.Hash())
	require.NoError(t, fw.wait())
	bc.framesQ.mu.Lock()
	require.Empty(t, bc.framesQ.byHash)
	bc.framesQ.mu.Unlock()
	require.NoError(t, fdb.View(context.Background(), func(tx kv.Tx) error {
		for i, h := range hashes {
			v, err := tx.GetOne(modules.Frames, h[:])
			require.NoError(t, err)
			require.Equal(t, raws[i], v)
		}
		return nil
	}))
	// A frame body missing a frame is not queued early (its hand-off writes
	// what it carries, as without the flag).
	hdr2 := &block.Header{Number: uint256.NewInt(2), GasLimit: 1, Difficulty: uint256.NewInt(1), BaseFee: uint256.NewInt(1)}
	hdr2.SetFrameTxRoot(block.FrameTxRoot(hashes))
	b2 := block.NewLaggedBlock(hdr2, hashes, [][]byte{nil, raws[1]})
	bc.startFramesWriteEarly(b2)
	bc.framesQ.mu.Lock()
	require.Empty(t, bc.framesQ.byHash)
	bc.framesQ.mu.Unlock()
	fw2 := bc.takeFramesWrite(b2)
	require.NotNil(t, fw2)
	require.NoError(t, fw2.wait())
}
