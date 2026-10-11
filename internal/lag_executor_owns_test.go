// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// P7: the receive paths leave a frame block under vote on availability to
// the lag executor (its write goes through the ordered writer); any other
// block, or the switch off, keeps the direct import.
func TestLagExecutorOwnsFrameBlocks(t *testing.T) {
	lc := newLagChain(t)
	hdr := &block.Header{Number: uint256.NewInt(5), Time: 100, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1)}
	frameBlk := block.NewLaggedBlock(hdr, []types.Hash{{0x01}}, [][]byte{{0x01}})

	restore := block.SetVoteOnAvailabilityForTest(false)
	require.False(t, lc.bc.LagExecutorOwns(frameBlk), "vote on availability off")
	restore()

	defer block.SetVoteOnAvailabilityForTest(true)()
	require.True(t, lc.bc.LagExecutorOwns(frameBlk))
	empty := block.NewBlock(&block.Header{Number: uint256.NewInt(6), Time: 110, Difficulty: uint256.NewInt(0)}, nil)
	require.True(t, lc.bc.LagExecutorOwns(empty), "an empty block is an availability block too")
}

// P7: a block the lag executor holds is not future-queued (the executor
// imports it once its parent is applied); any other block still is.
func TestAddFutureBlockLeavesLagQueuedBlocks(t *testing.T) {
	lc := newLagChain(t)
	lagStepPausedForTest.Store(true)
	defer lagStepPausedForTest.Store(false)
	mk := func(n uint64) *block.Block {
		return block.NewBlock(&block.Header{Number: uint256.NewInt(n), Time: 100 + n, Difficulty: uint256.NewInt(0)}, nil).(*block.Block)
	}
	queued, other := mk(7), mk(8)
	lc.bc.lagExec.mu.Lock()
	if lc.bc.lagExec.queued == nil {
		lc.bc.lagExec.queued = make(map[types.Hash]*lagQueued)
	}
	lc.bc.lagExec.queued[queued.Hash()] = &lagQueued{blk: queued}
	lc.bc.lagExec.mu.Unlock()
	require.NoError(t, lc.bc.AddFutureBlock(queued))
	require.NoError(t, lc.bc.AddFutureBlock(other))
	require.False(t, lc.bc.futureBlocks.Contains(queued.Hash()), "a lag-queued block was future-queued")
	require.True(t, lc.bc.futureBlocks.Contains(other.Hash()))
}
