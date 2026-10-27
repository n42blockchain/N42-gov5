// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// waitLagResultKnown waits until block hash's executed result is known
// here: in memory (its write pending) or stored -- what the lag-mode leader
// gate waits for before it builds on it.
func waitLagResultKnown(t *testing.T, bc *BlockChain, hash types.Hash) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, ok := bc.pendingExecutedResult(hash); ok {
			return
		}
		found := false
		_ = bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
			_, found, _ = rawdb.ReadExecutedResult(tx, hash)
			return nil
		})
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("executed result of %x never known", hash[:4])
		}
		time.Sleep(time.Millisecond)
	}
}

// buildOnApplied is lagChain.build with the reported result taken as the
// lag-mode leader's build takes it (ExecutedOrSeededResult): from memory
// while the block's write is pending.
func buildOnApplied(lc *lagChain, parent *block.Header, extra byte, txs ...*transaction.Transaction) *block.Block {
	hdr := &block.Header{ParentHash: parent.Hash(), Number: new(uint256.Int).AddUint64(parent.Number, 1),
		GasLimit: parent.GasLimit, Time: parent.Time + 10, BaseFee: uint256.NewInt(1), Difficulty: uint256.NewInt(1),
		Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111"), Extra: []byte{extra}}
	tx, err := lc.db.BeginRo(context.Background())
	require.NoError(lc.t, err)
	defer tx.Rollback()
	target, err := lc.bc.LaggedTarget(tx, hdr, nil)
	require.NoError(lc.t, err)
	res, err := lc.bc.ExecutedOrSeededResult(tx, target)
	require.NoError(lc.t, err)
	hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
	b := block.NewBlockFromReceipt(hdr, txs, nil, nil, nil).(*block.Block)
	lc.bc.RememberAvailable(b)
	return b
}

// lagQueueTransfers is block n's transactions: one transfer from each key
// (more than four, so the block runs ahead on Block-STM).
func lagQueueTransfers(lc *lagChain, n uint64) []*transaction.Transaction {
	txs := make([]*transaction.Transaction, 0, len(lc.keys))
	for i := range lc.keys {
		txs = append(txs, lc.transfer(i, n-1, lc.addrs[(i+1)%len(lc.addrs)], uint64(i+1)*n))
	}
	return txs
}

func lagQueueKeys(t *testing.T) []*ecdsa.PrivateKey {
	keys := make([]*ecdsa.PrivateKey, 6)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	return keys
}

type lagQueueRun struct {
	hashes   []types.Hash
	results  []rawdb.ExecutedResult
	root     types.Hash
	order    []uint64
	maxDepth int64
	failed   uint64
}

// runLagQueueChain imports blocks through the lag executor with every write
// slowed by delay and N42_LAG_WRITE_QUEUE=k, building each block as soon as
// the result it reports is known (the leader gate on the applied result),
// so the writes fall behind the imports. failAt > 0 fails that block's
// first write.
func runLagQueueChain(t *testing.T, keys []*ecdsa.PrivateKey, k, blocks int, delay time.Duration, failAt uint64) lagQueueRun {
	t.Helper()
	defer SetLagWriteQueueForTest(k)()
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	lc.bc.SetParallelEVM(true) // the ahead pipeline, as on the fleet
	var (
		mu    sync.Mutex
		run   lagQueueRun
		fired bool
	)
	lagWriteBeforeCommitForTest = func(n uint64) error {
		time.Sleep(delay)
		mu.Lock()
		defer mu.Unlock()
		run.order = append(run.order, n)
		if d := lc.bc.lagWrite.lastDepth.Load(); d > run.maxDepth {
			run.maxDepth = d
		}
		if n == failAt && !fired {
			fired = true
			return errors.New("injected write failure")
		}
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	for n := uint64(1); n <= uint64(blocks); n++ {
		if n >= 3 {
			waitLagResultKnown(t, lc.bc, blks[n-3].Hash())
		}
		b := buildOnApplied(lc, parent, 0, lagQueueTransfers(lc, n)...)
		lc.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
	}
	waitLagWritten(t, lc.bc, uint64(blocks))
	lc.bc.lock.Lock()
	require.NoError(t, lc.bc.joinLagWrite())
	require.Nil(t, lc.bc.lagWrite.older)
	require.Nil(t, lc.bc.lagWrite.olderPend.Load())
	run.root = lc.bc.qmdbRootComputer.Root()
	require.Equal(t, 0, lc.bc.qmdbRootComputer.StagedFlushes(), "a flush is still staged")
	lc.bc.lock.Unlock()
	for _, b := range blks {
		run.hashes = append(run.hashes, b.Hash())
		run.results = append(run.results, lc.result(b.Hash()))
	}
	_, run.failed, _ = lc.bc.LagWriteStats()
	require.NoError(t, lc.bc.Close())
	return run
}

// TestLagWriteQueueSlowWriterSameChain: with a writer slower than the
// imports, N42_LAG_WRITE_QUEUE=3 lets the hand-offs run ahead of the writes
// (two pending at a hand-off) and ends on the chain the one-write rule
// builds: the same block hashes, executed results (state roots, receipts
// roots, blooms, gas) and final tree root, written in block order.
func TestLagWriteQueueSlowWriterSameChain(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagPipelineForTest(true)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagWriteAsyncForTest(true)()
	keys := lagQueueKeys(t)
	const blocks = 10
	base := runLagQueueChain(t, keys, 1, blocks, 30*time.Millisecond, 0)
	queued := runLagQueueChain(t, keys, 3, blocks, 30*time.Millisecond, 0)
	require.LessOrEqual(t, base.maxDepth, int64(1), "the one-write rule left more than one write pending")
	require.GreaterOrEqual(t, queued.maxDepth, int64(2), "the queue never held two writes")
	require.Equal(t, base.hashes, queued.hashes)
	require.Equal(t, base.results, queued.results)
	require.Equal(t, base.root, queued.root)
	for i, n := range queued.order {
		require.Equal(t, uint64(i+1), n, "writes out of block order: %v", queued.order)
	}
}

// TestLagWriteQueueFailedWriteRequeues: a failed write with later writes
// queued behind it fails them all without writing; the tree is reloaded,
// every block is queued again and the chain ends where a chain whose writes
// never failed ends.
func TestLagWriteQueueFailedWriteRequeues(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagPipelineForTest(true)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagWriteAsyncForTest(true)()
	keys := lagQueueKeys(t)
	const blocks = 8
	want := runLagQueueChain(t, keys, 3, blocks, 20*time.Millisecond, 0)
	got := runLagQueueChain(t, keys, 3, blocks, 20*time.Millisecond, 4)
	require.GreaterOrEqual(t, got.failed, uint64(1), "the injected failure did not fire")
	require.Equal(t, want.hashes, got.hashes)
	require.Equal(t, want.results, got.results)
	require.Equal(t, want.root, got.root)
}

// TestLagWriteQueueRestartWithPendingWrites (restart rule under the
// queue): block 4's write holds, blocks 5 and 6 import and queue behind it
// (block 6 checked against block 4's result from memory). The process dies:
// block 4's transaction never commits and 5 and 6 are never written. On
// reopening the store is at block 3, and importing 4..6 again reaches the
// roots the first run computed in memory.
func TestLagWriteQueueRestartWithPendingWrites(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWriteQueueForTest(3)()
	defer SetLagPipelineForTest(true)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	keys := lagQueueKeys(t)
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	lc.bc.SetParallelEVM(true)
	const held, last = 4, 6
	reached := make(chan struct{})
	release := make(chan struct{})
	lagWriteBeforeCommitForTest = func(n uint64) error {
		if n != held {
			return nil
		}
		close(reached)
		<-release
		return errLagWriteInjected
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()

	parent := lc.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	for n := uint64(1); n <= last; n++ {
		if n >= 3 {
			waitLagResultKnown(t, lc.bc, blks[n-3].Hash())
		}
		b := buildOnApplied(lc, parent, 0, lagQueueTransfers(lc, n)...)
		lc.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
		if n == held {
			select {
			case <-reached:
			case <-time.After(30 * time.Second):
				t.Fatal("block 4 was never handed to the writer")
			}
		}
	}
	// Blocks 5 and 6 applied in memory, queued behind block 4's write.
	deadline := time.Now().Add(30 * time.Second)
	for !lc.bc.lagAppliedIs(blks[last-1].Hash(), last) {
		require.True(t, time.Now().Before(deadline), "block 6 never applied")
		time.Sleep(time.Millisecond)
	}
	memRoots := map[uint64]types.Hash{}
	for n := uint64(held); n <= last; n++ {
		j := lc.bc.lagWritePending(blks[n-1].Hash())
		require.NotNil(t, j, "block %d's write is not pending", n)
		memRoots[n] = j.root
		require.True(t, lc.bc.HasBlock(blks[n-1].Hash(), n))
		require.True(t, lc.bc.lagStoredApplied(blks[n-1].Hash(), n))
	}
	require.GreaterOrEqual(t, lc.bc.LagWriteQueueDepth(), 2)
	require.False(t, lc.bc.AppliedHeadIsExactly(blks[held-1].Hash(), held))

	// The crash: block 4 never commits; 5 and 6 fail behind it.
	// (The lag executor is paused so the recovery's re-queue does not write
	// the blocks again before the process "dies".)
	j6 := lc.bc.lagWritePending(blks[last-1].Hash())
	lagStepPausedForTest.Store(true)
	close(release)
	<-j6.done
	require.Error(t, j6.err)
	lagWriteBeforeCommitForTest = nil
	require.NoError(t, lc.bc.Close())
	lagStepPausedForTest.Store(false)

	re := reopenLagChain(t, lc)
	re.bc.SetParallelEVM(true)
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, uint64(held-1), an, "applied marker")
		require.Equal(t, blks[held-2].Hash(), types.Hash(ah))
		for n := uint64(held); n <= last; n++ {
			require.Nil(t, rawdb.ReadHeader(tx, blks[n-1].Hash(), n), "block %d's header is stored", n)
			_, found, err := rawdb.ReadExecutedResult(tx, blks[n-1].Hash())
			require.NoError(t, err)
			require.False(t, found, "block %d's executed result is stored", n)
		}
		return nil
	}))
	require.Equal(t, re.result(blks[held-2].Hash()).Root, re.bc.qmdbRootComputer.Root(), "reloaded tree root")

	for n := uint64(held); n <= last; n++ {
		re.bc.RememberAvailable(blks[n-1])
		re.bc.EnqueueLagImport(blks[n-1])
	}
	waitLagWritten(t, re.bc, last)
	for n := uint64(held); n <= last; n++ {
		require.Equal(t, memRoots[n], re.result(blks[n-1].Hash()).Root, "re-imported root of block %d", n)
	}
	re.bc.lock.Lock()
	require.NoError(t, re.bc.joinLagWrite())
	re.bc.lock.Unlock()
	require.Equal(t, memRoots[last], re.bc.qmdbRootComputer.Root())
	require.NoError(t, re.bc.Close())
}
