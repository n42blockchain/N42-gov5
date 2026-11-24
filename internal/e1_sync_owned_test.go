// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// e1LightPair is an executor serving its results to a light node, with one
// frame block imported on the executor: the light node holds its parent
// (genesis) and the executor's result is available to it.
type e1LightPair struct {
	light *lagChain
	blk   *block.Block
}

func newE1LightPair(t *testing.T) e1LightPair {
	t.Helper()
	t.Cleanup(SetFrameBlocksForTest(true))
	t.Cleanup(block.SetVoteOnAvailabilityForTest(true))
	t.Cleanup(block.SetDeferredDepthForTest(2, 0))
	t.Cleanup(commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn))
	t.Cleanup(SetLagPipelineForTest(true))
	t.Cleanup(SetLagWriteAsyncForTest(true))
	t.Cleanup(SetE1EarlyForTest(false))
	t.Cleanup(SetE1PrefetchForTest(false))
	t.Cleanup(SetE1FramesAheadForTest(false))
	t.Cleanup(SetE1LightLazyResolveForTest("off"))
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	t.Cleanup(func() { lagChainGasLimit = prevGas })

	keys := frametest.Keys(t, 65, 131)
	ex := newLagChainEngine(t, keys[1:], importBenchEngine{})
	ex.bc.SetParallelEVM(true)
	exStore := store.NewWithKeep(64<<20, 16)
	ex.bc.SetFrameStore(exStore)
	srv := httptest.NewServer(http.HandlerFunc(ex.bc.serveE1Result))
	t.Cleanup(srv.Close)

	light := newLagChainEngine(t, keys[1:], importBenchEngine{})
	light.bc.SetParallelEVM(true)
	lightStore := store.NewWithKeep(64<<20, 16)
	light.bc.SetFrameStore(lightStore)
	light.bc.SetE1SourceForTest(srv.URL)
	require.True(t, light.bc.E1Light())

	fb := frametest.NewBuilder(keys[0], ex.cfg.ChainID, keys[1:])
	parent := ex.bc.genesisBlock.Header().(*block.Header)
	raws, hashes, _ := fb.Frames(t, 2, 20)
	for i := range raws {
		require.True(t, exStore.Put(hashes[i], raws[i]))
		require.True(t, lightStore.Put(hashes[i], raws[i]))
	}
	hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
		Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
		Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
	hdr.Number.AddUint64(hdr.Number, 1)
	if bf := misc.CalcBaseFee(ex.cfg, parent); bf != nil {
		hdr.BaseFee.SetFromBig(bf)
	}
	var res rawdb.ExecutedResult
	require.Eventually(t, func() bool {
		rtx, err := ex.db.BeginRo(ex.bc.ctx)
		require.NoError(t, err)
		defer rtx.Rollback()
		res, _, err = ex.bc.LaggedExecutedResult(rtx, hdr, nil)
		return err == nil
	}, 30*time.Second, time.Millisecond)
	hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
	hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
	b := block.NewLaggedBlock(hdr, hashes, raws)
	checked, retry, err := ex.bc.CheckDeferredBlock(overWire(t, b))
	require.NoError(t, err)
	require.False(t, retry)
	require.True(t, checked)
	require.Eventually(t, func() bool { _, last, _ := ex.bc.LagStats(); return last >= 1 }, 30*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		ready, _, err := light.bc.LagReady(parent.Hash())
		require.NoError(t, err)
		return ready
	}, 30*time.Second, time.Millisecond)
	return e1LightPair{light: light, blk: b}
}

// TestE1LightStoresSyncOwnedHeight: on a light node the lag executor is the
// only importer (the sync importer's InsertChain only queues to it), so a
// height the sync pipeline owns in Range mode (the future insert gate) is
// still stored. Before the fix it stayed queued forever (bench run 98: a
// restarted light node never caught up).
func TestE1LightStoresSyncOwnedHeight(t *testing.T) {
	p := newE1LightPair(t)
	p.light.bc.SetFutureInsertGate(func(uint64) bool { return true })
	// The sync importer's path on a light chain: InsertChain queues.
	n, err := p.light.bc.InsertChain([]block.IBlock{overWire(t, p.blk)})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Eventually(t, func() bool { return p.light.bc.blockStored(p.blk.Hash(), 1) },
		10*time.Second, 5*time.Millisecond, "a sync-owned height was never stored by the light lag executor")
}

// TestE1LightSkipsAnchorHeldBlock: S1 enforce — a queued block the sync
// layer holds as an unanchored catch-up tail block is not stored by the
// light lag executor; once released it is.
func TestE1LightSkipsAnchorHeldBlock(t *testing.T) {
	p := newE1LightPair(t)
	var held atomic.Bool
	held.Store(true)
	h := p.blk.Hash()
	p.light.bc.SetLagHoldProbe(func(x types.Hash) bool { return held.Load() && x == h })
	_, err := p.light.bc.InsertChain([]block.IBlock{overWire(t, p.blk)})
	require.NoError(t, err)
	require.Never(t, func() bool { return p.light.bc.blockStored(h, 1) },
		time.Second, 5*time.Millisecond, "an anchor-held block was stored")
	require.True(t, p.light.bc.LagImportQueued(h), "an anchor-held block left the lag queue")
	held.Store(false)
	require.Eventually(t, func() bool { return p.light.bc.blockStored(h, 1) },
		10*time.Second, 5*time.Millisecond, "a released block was not stored")
}
