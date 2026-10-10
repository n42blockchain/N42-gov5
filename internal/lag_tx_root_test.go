// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/rlp"
)

// lagBuild assembles a child of parent as the lag-mode leader does
// (miner.commitWorkLagged + the HotStuff adapter's AssembleWithoutExecution):
// lagged execution fields, the frame root only when there are frames, and
// block.NewLaggedBlock.
func lagBuild(t *testing.T, lc *lagChain, parent *block.Header, hashes []types.Hash, raws [][]byte) *block.Block {
	t.Helper()
	hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
		Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
		Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
	hdr.Number.AddUint64(hdr.Number, 1)
	tx, err := lc.db.BeginRo(lc.bc.ctx)
	require.NoError(t, err)
	res, _, err := lc.bc.LaggedExecutedResult(tx, hdr, nil)
	tx.Rollback()
	require.NoError(t, err)
	hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
	if len(hashes) > 0 {
		hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
	}
	b := block.NewLaggedBlock(hdr, hashes, raws)
	lc.bc.RememberAvailable(b)
	return b
}

func parentDifficulty(p *block.Header) *uint256.Int { return uint256.NewInt(1) }

// overWire round-trips a block through its wire form (the push).
func overWire(t *testing.T, b *block.Block) *block.Block {
	t.Helper()
	data, err := rlp.EncodeToBytes(b)
	require.NoError(t, err)
	var out block.Block
	require.NoError(t, rlp.DecodeBytes(data, &out))
	return &out
}

// TestLagBuiltBlocksImportOnAFollower reproduces S92: at depth 2 with frame
// blocks and the availability vote, the leader's empty block (no frames, no
// pool transactions, the warm-up decay) stamped the frame root of zero
// frames, keccak of nothing, and every follower's verifier -- which reads a
// body without frame hashes as an ordinary body -- rejected it as a bad
// block. Built the leader's way, an empty block and a one-frame block both
// pass the follower's availability check and import on a second chain, and
// both chains execute them to the same results.
func TestLagBuiltBlocksImportOnAFollower(t *testing.T) {
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	keys := frametest.Keys(t, 3, 61)
	leader := newLagChainKeys(t, keys[1:])
	follower := newLagChainKeys(t, keys[1:])
	require.Equal(t, leader.bc.genesisBlock.Hash(), follower.bc.genesisBlock.Hash())
	fb := frametest.NewBuilder(keys[0], leader.cfg.ChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 1, 4)
	for _, lc := range []*lagChain{leader, follower} {
		st := store.New(1 << 24)
		st.Put(hashes[0], raws[0])
		lc.bc.SetFrameStore(st)
	}
	g := leader.bc.genesisBlock.Header().(*block.Header)

	empty := lagBuild(t, leader, g, nil, nil)
	require.Equal(t, block.TxRootAt(nil, empty.Time()), empty.TxHash(), "an empty lag block is an ordinary empty block")
	one := lagBuild(t, leader, empty.Header().(*block.Header), hashes, raws)
	require.Equal(t, block.FrameTxRoot(hashes), one.TxHash())

	for _, b := range []*block.Block{empty, one} {
		w := overWire(t, b)
		checked, retry, err := follower.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", b.Number64().Uint64())
		_, err = follower.bc.InsertChainAuthorized([]block.IBlock{w})
		require.NoError(t, err, "follower import of block %d", b.Number64().Uint64())
		require.True(t, follower.bc.blockStored(b.Hash(), b.Number64().Uint64()))
		leader.insert(b)
		require.Equal(t, leader.result(b.Hash()), follower.result(b.Hash()))
	}
	require.Equal(t, 4, len(one.Transactions()))
}
