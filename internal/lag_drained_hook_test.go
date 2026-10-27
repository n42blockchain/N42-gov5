// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestLagDrainedHookRunsAfterLastWrite: Close runs the drained hook once
// the write in flight at shutdown is durable, with the chain context still
// live, so consensus can canonicalize a commit it deferred on that write.
func TestLagDrainedHookRunsAfterLastWrite(t *testing.T) {
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

	const last = 3
	reached := make(chan struct{})
	release := make(chan struct{})
	lagWriteBeforeCommitForTest = func(n uint64) error {
		if n == last {
			close(reached)
			<-release
		}
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()

	var hookApplied uint64
	var hookCtxErr error
	calls := 0
	lc.bc.SetLagDrainedHook(func() {
		calls++
		hookCtxErr = lc.bc.ctx.Err()
		_ = lc.db.View(context.Background(), func(tx kv.Tx) error {
			hookApplied, _, _, _ = rawdb.ReadQMDBApplied(tx)
			return nil
		})
	})

	parent := lc.bc.genesisBlock.Header().(*block.Header)
	for n := uint64(1); n <= last; n++ {
		if n >= 3 {
			waitLagWritten(t, lc.bc, n-2)
		}
		b := lc.build(parent, 0, lc.transfer(0, n-1, a1, 1000+n))
		lc.bc.EnqueueLagImport(b)
		parent = b.Header().(*block.Header)
	}
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the last block was never handed to the writer")
	}
	closed := make(chan error, 1)
	go func() { closed <- lc.bc.Close() }()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, calls, "the hook ran before the write in flight finished")
	close(release)
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not return")
	}
	require.Equal(t, 1, calls)
	require.NoError(t, hookCtxErr, "the hook ran on a cancelled chain context")
	require.Equal(t, uint64(last), hookApplied, "the hook ran before the last write was durable")
}
