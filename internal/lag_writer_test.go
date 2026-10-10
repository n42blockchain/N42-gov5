// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// waitLagWritten waits until the lag executor reported block n (durable).
func waitLagWritten(t *testing.T, bc *BlockChain, n uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, last, _ := bc.LagStats(); last >= n {
			return
		}
		if time.Now().After(deadline) {
			_, last, q := bc.LagStats()
			t.Fatalf("lag executor reported %d (queued %d), want %d", last, q, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// reopenLagChain is a restart: a new chain and a new QMDB computer loaded
// from what lc's store holds, as the node builds them at startup.
func reopenLagChain(t *testing.T, lc *lagChain) *lagChain {
	t.Helper()
	ctx := context.Background()
	gb := lc.bc.genesisBlock
	bci, err := NewBlockChain(ctx, gb, importBenchEngine{}, lc.db, nil, lc.cfg)
	require.NoError(t, err)
	re := *lc
	re.bc = bci.(*BlockChain)
	rc := commitment.NewQMDBRootComputer()
	rtx, err := lc.db.BeginRo(ctx)
	require.NoError(t, err)
	rc.SetCold(rtx)
	require.NoError(t, rc.LoadFrom(rtx))
	rtx.Rollback()
	rc.SetCold(nil)
	rc.EnableUndoRecording()
	re.bc.SetQMDBRootComputer(rc)
	return &re
}

// TestLagWriterRestartBeforeLastWrite (P4b restart rule): blocks imported
// through the lag executor with the ordered writer, the process dies while
// the last block's write is in flight (its transaction never commits). On
// reopening, the store is at the block before it -- header, applied marker,
// executed result, QMDB undo window and changesets all one block behind,
// the reloaded tree at that block's root -- and importing the lost block
// again reaches the root the first run had computed in memory; the chain
// then goes on.
func TestLagWriterRestartBeforeLastWrite(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	a1 := lc.addrs[1]

	// The write of block 4 "crashes": it holds until released, then never
	// commits.
	const lost = 4
	reached := make(chan struct{})
	release := make(chan struct{})
	lagWriteBeforeCommitForTest = func(n uint64) error {
		if n != lost {
			return nil
		}
		close(reached)
		<-release
		return errLagWriteInjected
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()

	g := lc.bc.genesisBlock.Header().(*block.Header)
	var blks []*block.Block
	parent := g
	for n := uint64(1); n <= lost; n++ {
		// The leader gate: block n reports n-2.
		if n >= 3 {
			waitLagWritten(t, lc.bc, n-2)
		}
		b := lc.build(parent, 0, lc.transfer(0, n-1, a1, 1000+n), lc.transfer(1, n-1, a1, 7))
		lc.bc.EnqueueLagImport(b)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
	}
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("block 4 was never handed to the writer")
	}
	waitLagWritten(t, lc.bc, lost-1)
	job := lc.bc.lagWritePending(blks[lost-1].Hash())
	require.NotNil(t, job, "block 4's write is not in flight")
	memRoot, ok := job.ibs.LastIntermediateRoot()
	require.True(t, ok)
	require.Equal(t, memRoot, job.root, "the tree after block 4 is its executed root")
	// While it is in flight the block reads as known, its header resolves,
	// and the durable marker is still the block before it.
	require.True(t, lc.bc.HasBlock(blks[lost-1].Hash(), lost))
	require.False(t, lc.bc.AppliedHeadIsExactly(blks[lost-1].Hash(), lost))
	require.True(t, lc.bc.lagAppliedIs(blks[lost-1].Hash(), lost))

	// The crash: the transaction never commits; nothing joins the write.
	close(release)
	<-job.done
	lagWriteBeforeCommitForTest = nil
	require.NoError(t, lc.bc.Close())

	re := reopenLagChain(t, lc)
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, uint64(lost-1), an, "applied marker")
		require.Equal(t, blks[lost-2].Hash(), types.Hash(ah))
		require.Nil(t, rawdb.ReadHeader(tx, blks[lost-1].Hash(), lost), "the lost block's header is stored")
		_, found, err := rawdb.ReadExecutedResult(tx, blks[lost-1].Hash())
		require.NoError(t, err)
		require.False(t, found, "the lost block's executed result is stored")
		hc, err := highestChangesetBlock(tx)
		require.NoError(t, err)
		require.Equal(t, uint64(lost-1), hc, "changesets")
		v, err := tx.GetOne(modules.QMDBUndoWindow, modules.EncodeBlockNumber(lost))
		require.NoError(t, err)
		require.Nil(t, v, "the lost block's undo row is stored")
		v, err = tx.GetOne(modules.QMDBUndoWindow, modules.EncodeBlockNumber(lost-1))
		require.NoError(t, err)
		require.NotNil(t, v, "block 3's undo row is missing")
		return nil
	}))
	require.Equal(t, re.result(blks[lost-2].Hash()).Root, re.bc.qmdbRootComputer.Root(), "reloaded tree root")
	require.False(t, re.bc.HasBlock(blks[lost-1].Hash(), lost))

	// The lost block imports again, to the same root.
	re.bc.RememberAvailable(blks[lost-1])
	re.bc.EnqueueLagImport(blks[lost-1])
	waitLagWritten(t, re.bc, lost)
	require.Equal(t, memRoot, re.result(blks[lost-1].Hash()).Root, "re-imported root")
	// And the chain goes on: block 5 reports block 3, block 6 block 4.
	b5 := re.build(blks[lost-1].Header().(*block.Header), 0, re.transfer(0, lost, a1, 1))
	re.bc.EnqueueLagImport(b5)
	waitLagWritten(t, re.bc, lost+1)
	b6 := re.build(b5.Header().(*block.Header), 0, re.transfer(0, lost+1, a1, 1))
	require.Equal(t, memRoot, b6.Header().(*block.Header).Root)
	re.bc.EnqueueLagImport(b6)
	waitLagWritten(t, re.bc, lost+2)
	re.bc.lock.Lock()
	require.NoError(t, re.bc.joinLagWrite())
	re.bc.lock.Unlock()
	require.Equal(t, re.result(b6.Hash()).Root, re.bc.qmdbRootComputer.Root())
}

// TestLagWriterFailedWriteRequeues: a handed-off write that fails leaves
// memory ahead of the store; the next import joins it, reloads the tree
// from the store and queues the block again, and the chain ends where a
// chain whose writes never failed ends.
func TestLagWriterFailedWriteRequeues(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	run := func(failAt uint64) (roots []types.Hash, failed uint64) {
		lc := newLagChainEngine(t, keys, importBenchEngine{})
		fired := false
		lagWriteBeforeCommitForTest = func(n uint64) error {
			if n == failAt && !fired {
				fired = true
				return errors.New("injected write failure")
			}
			return nil
		}
		defer func() { lagWriteBeforeCommitForTest = nil }()
		parent := lc.bc.genesisBlock.Header().(*block.Header)
		var blks []*block.Block
		const blocks = 6
		for n := uint64(1); n <= blocks; n++ {
			if n >= 3 {
				waitLagWritten(t, lc.bc, n-2)
			}
			b := lc.build(parent, 0, lc.transfer(0, n-1, lc.addrs[1], 10*n), lc.transfer(2, n-1, lc.addrs[0], n))
			lc.bc.EnqueueLagImport(b)
			blks = append(blks, b)
			parent = b.Header().(*block.Header)
		}
		waitLagWritten(t, lc.bc, blocks)
		for _, b := range blks {
			roots = append(roots, lc.result(b.Hash()).Root)
		}
		_, failed, _ = lc.bc.LagWriteStats()
		require.NoError(t, lc.bc.Close())
		return roots, failed
	}
	want, _ := run(0)
	got, failed := run(3)
	require.Equal(t, uint64(1), failed, "the injected failure did not fire")
	require.Equal(t, want, got)
}
