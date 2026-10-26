// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// TestE1LightFollowsExecutor: under deferred depth 2 an executor imports a
// chain of frame blocks while a light node (N42_E1_SOURCE) queued with the
// same blocks stores each one with the result the executor serves. The
// light node executes nothing (no ahead run, no applied QMDB head), its
// stored results equal the executor's, its leader gate opens on them and
// its availability check of the next header passes against them.
func TestE1LightFollowsExecutor(t *testing.T) {
	const (
		blocks   = 6
		frames   = 2
		perFrame = 40
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	keys := frametest.Keys(t, 65, 131)
	ex := newLagChainEngine(t, keys[1:], importBenchEngine{})
	ex.bc.SetParallelEVM(true)
	exStore := store.NewWithKeep(64<<20, 16)
	ex.bc.SetFrameStore(exStore)

	srv := httptest.NewServer(http.HandlerFunc(ex.bc.serveE1Result))
	defer srv.Close()

	light := newLagChainEngine(t, keys[1:], importBenchEngine{})
	require.Equal(t, ex.bc.genesisBlock.Hash(), light.bc.genesisBlock.Hash())
	light.bc.SetParallelEVM(true)
	lightStore := store.NewWithKeep(64<<20, 16)
	light.bc.SetFrameStore(lightStore)
	light.bc.SetE1SourceForTest(srv.URL)
	require.True(t, light.bc.E1Light())
	require.False(t, ex.bc.E1Light())

	fb := frametest.NewBuilder(keys[0], ex.cfg.ChainID, keys[1:])
	build := func(parent *block.Header) *block.Block {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
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
		// The result the header carries is stored on the executor.
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
		return block.NewLaggedBlock(hdr, hashes, raws)
	}
	check := func(bc *BlockChain, b *block.Block) {
		checked, retry, err := bc.CheckDeferredBlock(overWire(t, b))
		require.NoError(t, err)
		require.False(t, retry)
		require.True(t, checked)
	}

	var chain []*block.Block
	parent := ex.bc.genesisBlock.Header().(*block.Header)
	for i := 0; i < blocks; i++ {
		b := build(parent)
		check(ex.bc, b)
		// The light node waits on the executor for this block's result.
		require.Eventually(t, func() bool {
			ready, _, err := light.bc.LagReady(parent.Hash())
			require.NoError(t, err)
			return ready
		}, 30*time.Second, time.Millisecond, "light gate for block %d", i+1)
		check(light.bc, b)
		chain = append(chain, b)
		parent = b.Header().(*block.Header)
	}

	require.Eventually(t, func() bool { _, last, _ := ex.bc.LagStats(); return last >= blocks }, 30*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { _, last, q := light.bc.LagStats(); return last >= blocks && q == 0 }, 30*time.Second, time.Millisecond)

	read := func(lc *lagChain, h types.Hash) (rawdb.ExecutedResult, bool) {
		var (
			r  rawdb.ExecutedResult
			ok bool
		)
		require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
			var err error
			r, ok, err = rawdb.ReadExecutedResult(tx, h)
			return err
		}))
		return r, ok
	}
	for i, b := range chain {
		want, ok := read(ex, b.Hash())
		require.True(t, ok, "executor result of block %d", i+1)
		got, ok := read(light, b.Hash())
		require.True(t, ok, "light result of block %d", i+1)
		require.Equal(t, want, got, "block %d", i+1)
		require.True(t, light.bc.blockStored(b.Hash(), uint64(i+1)))
	}

	launched, _, _, _ := light.bc.LagPipelineStats()
	require.Zero(t, launched, "the light node ran a block ahead")
	require.NoError(t, light.db.View(context.Background(), func(tx kv.Tx) error {
		_, _, applied, err := rawdb.ReadQMDBApplied(tx)
		require.False(t, applied, "the light node applied state")
		return err
	}))
	imported, _, _, _ := E1LightStats()
	require.GreaterOrEqual(t, imported, uint64(blocks))
}

// TestE1ServeResult: the executor answers a stored result, 204 for a hash it
// does not know within the wait, and 400 without a hash.
func TestE1ServeResult(t *testing.T) {
	lc := newLagChainEngine(t, nil, importBenchEngine{})
	known := types.HexToHash("0x01")
	want := rawdb.ExecutedResult{Root: types.HexToHash("0xaa"), ReceiptHash: types.HexToHash("0xbb"), GasUsed: 21000}
	want.Bloom[3] = 7
	require.NoError(t, lc.db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteExecutedResult(tx, known, want)
	}))
	srv := httptest.NewServer(http.HandlerFunc(lc.bc.serveE1Result))
	defer srv.Close()

	got, ok, err := e1Fetch(context.Background(), srv.URL, known)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, got)

	t0 := time.Now()
	_, ok, err = e1Fetch(context.Background(), srv.URL, types.HexToHash("0x02"))
	require.NoError(t, err)
	require.False(t, ok)
	require.GreaterOrEqual(t, time.Since(t0), e1FetchWait/2, "the poll did not wait")

	resp, err := http.Get(srv.URL + "/e1/result")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
