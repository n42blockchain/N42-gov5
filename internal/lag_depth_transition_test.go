// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// depthTransitionFrom is the first height at depth 3 in the transition
// tests; below it the chain runs at depth 2 (N42_DEFERRED_DEPTH_PREV=2).
const depthTransitionFrom = 6

// reportedBy is the number of the block whose executed result header n
// carries under the per-height depth.
func reportedBy(n uint64) uint64 {
	k := uint64(block.DeferredDepthAt(n))
	if n < k {
		return 0
	}
	return n - k
}

// waitLagReady polls the leader gate for a child of parent.
func waitLagReady(t *testing.T, bc *BlockChain, parent types.Hash) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ready, waitFor, err := bc.LagReady(parent)
		require.NoError(t, err)
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lag gate on %x never opened (waits for %x)", parent[:4], waitFor[:4])
		}
		time.Sleep(time.Millisecond)
	}
}

// waitResultStored waits until lc stores its executed result of hash (the
// executor's counters restart with the chain, the store does not).
func waitResultStored(t *testing.T, lc *lagChain, hash types.Hash) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var ok bool
		require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
			var err error
			_, ok, err = rawdb.ReadExecutedResult(tx, hash)
			return err
		}))
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("executed result of %x never stored", hash[:4])
		}
		time.Sleep(time.Millisecond)
	}
}

// settleLag waits until the lag executor stored b's result and its write
// landed.
func settleLag(t *testing.T, lc *lagChain, b *block.Block) {
	t.Helper()
	waitResultStored(t, lc, b.Hash())
	lc.bc.lock.Lock()
	require.NoError(t, lc.bc.joinLagWrite())
	lc.bc.lock.Unlock()
}

// TestLagDepthTransitionTwoToThree runs a depth-2 chain that switches to
// depth 3 at depthTransitionFrom. A leader chain builds every block without
// executing it (behind the lag gate) and imports it through the lag
// executor; a follower chain imports the same blocks, which checks every
// header against its own execution. Every header n must carry the executed
// result of block n-k(n): headers FROM-1 and FROM both report FROM-3, the
// gate for FROM asks for nothing FROM-1 did not, and no block's result is
// skipped. The restart cases close and reopen both chains just below and
// just above the boundary and continue on the reopened stores.
func TestLagDepthTransitionTwoToThree(t *testing.T) {
	for _, restartAt := range []uint64{0, depthTransitionFrom - 1, depthTransitionFrom, depthTransitionFrom + 1} {
		name := "noRestart"
		if restartAt > 0 {
			name = fmt.Sprintf("restartAt%d", restartAt)
		}
		t.Run(name, func(t *testing.T) { testLagDepthTransition(t, restartAt) })
	}
}

func testLagDepthTransition(t *testing.T, restartAt uint64) {
	const last = depthTransitionFrom + 8
	defer block.SetDeferredDepthTransitionForTest(2, 3, depthTransitionFrom)()
	defer SetLagWriteAsyncForTest(true)()

	leader := newLagChainEngine(t, nil, importBenchEngine{})
	follower := newLagChainEngine(t, leader.keys, importBenchEngine{})
	require.Equal(t, leader.bc.genesisBlock.Hash(), follower.bc.genesisBlock.Hash())
	a1 := leader.addrs[1]

	blks := []*block.Block{leader.bc.genesisBlock.(*block.Block)}
	parent := blks[0].Header().(*block.Header)
	for n := uint64(1); n <= last; n++ {
		// The leader gate: block n-k(n) executed here.
		if tgt := reportedBy(n); tgt > 0 {
			waitResultStored(t, leader, blks[tgt].Hash())
		}
		waitLagReady(t, leader.bc, parent.Hash())
		if n == depthTransitionFrom {
			// FROM reports what FROM-1 reported: FROM-3.
			require.Equal(t, reportedBy(n-1), reportedBy(n))
			require.Equal(t, uint64(depthTransitionFrom-3), reportedBy(n))
		}
		b := leader.build(parent, 0, leader.transfer(0, n-1, a1, 1000+n))
		hdr := b.Header().(*block.Header)
		tgt := blks[reportedBy(n)]
		rtx, err := leader.db.BeginRo(context.Background())
		require.NoError(t, err)
		_, target, err := leader.bc.LaggedExecutedResult(rtx, hdr, nil)
		rtx.Rollback()
		require.NoError(t, err)
		require.Equal(t, tgt.Hash(), target.Hash(), "header %d reports block %d", n, reportedBy(n))

		leader.bc.EnqueueLagImport(b)
		follower.bc.RememberAvailable(b)
		follower.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = hdr

		if n == restartAt {
			for _, lc := range []*lagChain{leader, follower} {
				settleLag(t, lc, b)
				for _, cb := range blks[1:] {
					require.NoError(t, lc.bc.CommitToCanonical(cb.Hash()))
				}
				require.NoError(t, lc.bc.Close())
			}
			leader = reopenLagChain(t, leader)
			follower = reopenLagChain(t, follower)
			for _, lc := range []*lagChain{leader, follower} {
				lc.bc.revertSpeculativeOnStartup()
				require.True(t, lc.bc.AppliedHeadIsExactly(b.Hash(), n), "restart keeps the applied head")
			}
		}
	}
	settleLag(t, leader, blks[last])
	settleLag(t, follower, blks[last])

	// Every header carries the real executed result of n-k(n), on both
	// chains, and the follower's execution equals the leader's.
	seen := map[uint64]int{}
	for n := uint64(1); n <= last; n++ {
		hdr := blks[n].Header().(*block.Header)
		tn := reportedBy(n)
		seen[tn]++
		require.Equal(t, leader.result(blks[n].Hash()), follower.result(blks[n].Hash()), "block %d executes alike", n)
		if tn == 0 {
			continue
		}
		want := leader.result(blks[tn].Hash())
		require.Equal(t, want.Root, hdr.Root, "header %d root", n)
		require.Equal(t, want.ReceiptHash, hdr.ReceiptHash, "header %d receipts root", n)
		require.Equal(t, want.Bloom, hdr.Bloom, "header %d bloom", n)
		require.Equal(t, want.GasUsed, hdr.GasUsed, "header %d gas used", n)
		require.NotZero(t, want.GasUsed, "block %d executed its transfer", tn)
	}
	// No result skipped; only FROM-3 is reported twice.
	for tn := uint64(1); tn <= last-3; tn++ {
		want := 1
		if tn == depthTransitionFrom-3 {
			want = 2
		}
		require.Equal(t, want, seen[tn], "block %d reported %d times", tn, seen[tn])
	}
	// Every transfer executed exactly once.
	got := new(uint256.Int).Sub(follower.balance(a1), follower.balanceAtGenesis())
	var sum uint64
	for n := uint64(1); n <= last; n++ {
		sum += 1000 + n
	}
	require.Equal(t, sum, got.Uint64())
}

// TestDeferredHeaderCheckDepthTransition: across a 2 -> 3 switch at FROM,
// header FROM-1 must carry FROM-3's result (depth 2), header FROM must carry
// FROM-3's result again (depth 3) and refuses FROM-2's (the depth-2 rule),
// and header FROM+1 carries FROM-2's. The leader gate for FROM waits on
// FROM-3, as the one for FROM-1 did.
func TestDeferredHeaderCheckDepthTransition(t *testing.T) {
	const from = 20
	defer block.SetDeferredDepthTransitionForTest(2, 3, from)()
	cfg := &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}
	db := memdb.NewTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), chainConfig: cfg}

	// Heights from-4 .. from+1; results stored for from-4 .. from-2.
	hs := map[uint64]*block.Header{}
	res := map[uint64]rawdb.ExecutedResult{}
	var ph types.Hash
	for n := uint64(from - 4); n <= from+1; n++ {
		h := &block.Header{Number: uint256.NewInt(n), Time: 1000 + n, ParentHash: ph, GasUsed: n}
		res[n] = rawdb.ExecutedResult{Root: types.Hash{byte(n)}, GasUsed: 100 + n}
		if t2 := n - uint64(block.DeferredDepthAt(n)); n >= from-2 {
			h.Root, h.GasUsed = res[t2].Root, res[t2].GasUsed
		}
		hs[n] = h
		ph = h.Hash()
	}
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		for n := uint64(from - 4); n <= from+1; n++ {
			rawdb.WriteHeader(tx, hs[n])
			if n <= from-2 {
				if err := rawdb.WriteExecutedResult(tx, hs[n].Hash(), res[n]); err != nil {
					return err
				}
			}
		}
		return nil
	}))
	tx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, checkDeferredHeader(cfg, tx, hs[from-1], hs[from-2]), "FROM-1 carries FROM-3 (depth 2)")
	require.NoError(t, checkDeferredHeader(cfg, tx, hs[from], hs[from-1]), "FROM carries FROM-3 (depth 3)")
	require.Equal(t, hs[from-1].Root, hs[from].Root)
	require.NoError(t, checkDeferredHeader(cfg, tx, hs[from+1], hs[from]), "FROM+1 carries FROM-2")
	bad := *hs[from]
	bad.Root, bad.GasUsed = res[from-2].Root, res[from-2].GasUsed
	require.Error(t, checkDeferredHeader(cfg, tx, &bad, hs[from-1]), "FROM carrying FROM-2 (the depth-2 rule) is refused")

	// The gate for FROM (parent FROM-1) is ready: FROM-3 is executed.
	ready, _, err := bc.LagReady(hs[from-1].Hash())
	require.NoError(t, err)
	require.True(t, ready)
	// The gate for FROM+2 (parent FROM+1) waits for FROM-1.
	ready, waitFor, err := bc.LagReady(hs[from+1].Hash())
	require.NoError(t, err)
	require.False(t, ready)
	require.Equal(t, hs[from-1].Hash(), waitFor)
}
