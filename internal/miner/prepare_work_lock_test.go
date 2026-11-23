// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
)

// writerQueuingChain makes a writer queue on the worker's mutex from inside
// prepareWork's critical section: CurrentBlock is the first call prepareWork
// makes after taking w.mu.RLock.
type writerQueuingChain struct {
	common.IBlockChain
	w    *worker
	once sync.Once
}

func (c *writerQueuingChain) CurrentBlock() block.IBlock {
	c.once.Do(func() {
		go func() {
			c.w.mu.Lock()
			//nolint:staticcheck // empty critical section: only the queuing matters
			c.w.mu.Unlock()
		}()
		// A pending writer makes TryRLock fail; wait until it is queued.
		for c.w.mu.TryRLock() {
			c.w.mu.RUnlock()
			time.Sleep(time.Millisecond)
		}
	})
	return c.IBlockChain.CurrentBlock()
}

// prepareWork holds w.mu.RLock for its whole body. sync.RWMutex is not
// reentrant: a second RLock behind a queued writer never returns, and the
// writer never gets the lock either. A validator hit exactly this on
// 2026-11-22 (workLoop's clearPending queued between prepareWork's RLock and
// its ownSealed lookup) and stopped producing blocks until restarted.
func TestPrepareWorkDoesNotReacquireLockBehindWriter(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	w.chain = &writerQueuingChain{IBlockChain: f.Chain, w: w}

	errCh := make(chan error, 1)
	go func() {
		_, err := w.prepareWork(&generateParams{
			timestamp:  uint64(time.Now().Unix()),
			coinbase:   f.Coinbase,
			parentHash: f.Chain.CurrentBlock().Hash(),
		})
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("prepareWork: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prepareWork deadlocked: it took w.mu.RLock again while a writer was queued")
	}
}
