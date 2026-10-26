// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestMVSCheckCleanBlock runs a bench-shaped transfer block (large enough
// for the presized store) with N42_MVS_CHECK on and requires the check ran
// and found nothing.
func TestMVSCheckCleanBlock(t *testing.T) {
	defer parallel.SetMVSCheckForTest(true)()
	defer setTransferFast2ForTest(true)()
	nTxs, senders := 12000, 600
	if v, err := strconv.Atoi(os.Getenv("N42_TEST_MVS_CHECK_TXS")); err == nil && v > 0 {
		nTxs, senders = v, v/20
	}
	fx := newTransferBlockFixture(nTxs, senders, 22857, 1.1, 50)
	db := memdb.NewTestDB(t)
	fundTransferBlockStateFiller(t, db, fx, 100)
	b0, f0, n0, a0 := mvsCheckBlocks.Load(), mvsCheckFailedBlocks.Load(), mvsCheckNanos.Load(), mvsCheckApplyNanos.Load()
	for _, presize := range []bool{false, true} {
		restore := parallel.SetPresizeForTest(presize)
		bc := &BlockChain{ctx: context.Background(), ChainDB: db}
		sp := NewStateProcessor(fx.cfg, bc, nil)
		blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
		require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
			ibs := state.New(state.NewPlainStateReader(tx))
			_, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
			return err
		}))
		restore()
	}
	require.Equal(t, int64(2), mvsCheckBlocks.Load()-b0, "check did not run on both blocks")
	t.Logf("%d txs: check %.2f ms per block (apply walk part %.2f ms)", nTxs, float64(mvsCheckNanos.Load()-n0)/2/1e6, float64(mvsCheckApplyNanos.Load()-a0)/2/1e6)
	require.Zero(t, mvsCheckFailedBlocks.Load()-f0, "check reported violations on a clean block")
}

// TestMVSCheckApplyDroppedDelta builds a store crediting two recipients,
// then plays the apply walk with one credit short of its entry, and a fold
// that drops one, and requires check 3 to fire on each and only then.
func TestMVSCheckApplyDroppedDelta(t *testing.T) {
	r1, r2 := types.HexToAddress("0x1001"), types.HexToAddress("0x1002")
	mvs := parallel.NewMVS()
	for i := 0; i < 5; i++ {
		mvs.WriteDelta(parallel.LocationKey{Address: r1, Field: parallel.FieldBalance}, i, 0, uint256.NewInt(1))
		mvs.WriteDelta(parallel.LocationKey{Address: r2, Field: parallel.FieldBalance}, i, 0, uint256.NewInt(2))
	}
	run := func(walk1 uint64, fold1 uint64) int {
		db := memdb.NewTestDB(t)
		var issues int
		require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
			ibs := state.New(state.NewPlainStateReader(tx))
			chk := &mvsApplyCheck{block: 7, numTxs: 5}
			ps := []mvsCreditProbe{
				{addr: r1, delta: uint256.NewInt(walk1)},
				{addr: r2, delta: uint256.NewInt(10)},
			}
			chk.before(ps, ibs)
			ibs.AddBalance(r1, uint256.NewInt(fold1))
			ibs.AddBalance(r2, uint256.NewInt(10))
			chk.after(ps, mvs, 5, ibs)
			issues = chk.issues
			return nil
		}))
		return issues
	}
	require.Zero(t, run(5, 5), "clean walk and fold")
	require.Equal(t, 1, run(4, 4), "walk dropped one delta: check 3 must fire")
	require.Equal(t, 1, run(5, 4), "fold dropped one delta: check 3 must fire")

	// A duplicated address in the walk.
	db := memdb.NewTestDB(t)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		chk := &mvsApplyCheck{block: 7, numTxs: 5}
		chk.before([]mvsCreditProbe{{addr: r1, delta: uint256.NewInt(4)}, {addr: r1, delta: uint256.NewInt(1)}}, ibs)
		require.Equal(t, 1, chk.issues, "duplicate address not reported")
		return nil
	}))
}
