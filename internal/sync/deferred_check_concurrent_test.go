// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// orderTestChain runs CheckDeferredBlock and InsertChain each with an
// optional delay, recording when each started/finished (protected by mu),
// and reports HasAppliedBlock=true once InsertChain has returned
// successfully -- enough for blockApplied to let handlePushedBlock's own
// NotifyBlockImported fire in these tests without a real chain.
type orderTestChain struct {
	common.IBlockChain

	checkDelay time.Duration
	checkErr   error

	insertDelay time.Duration
	insertErr   error

	mu          sync.Mutex
	checkStart  time.Time
	checkDone   time.Time
	insertStart time.Time
	insertDone  time.Time
	inserted    bool
	checkDoneCh chan struct{}
}

func newOrderTestChain() *orderTestChain {
	return &orderTestChain{checkDoneCh: make(chan struct{}, 1)}
}

func (c *orderTestChain) CheckDeferredBlock(block.IBlock) (bool, bool, error) {
	c.mu.Lock()
	c.checkStart = time.Now()
	c.mu.Unlock()
	if c.checkDelay > 0 {
		time.Sleep(c.checkDelay)
	}
	c.mu.Lock()
	c.checkDone = time.Now()
	c.mu.Unlock()
	c.checkDoneCh <- struct{}{}
	return true, false, c.checkErr
}

func (c *orderTestChain) InsertChain([]block.IBlock) (int, error) {
	c.mu.Lock()
	c.insertStart = time.Now()
	c.mu.Unlock()
	if c.insertDelay > 0 {
		time.Sleep(c.insertDelay)
	}
	c.mu.Lock()
	c.insertDone = time.Now()
	if c.insertErr == nil {
		c.inserted = true
	}
	c.mu.Unlock()
	return 0, c.insertErr
}

func (c *orderTestChain) HasAppliedBlock(types.Hash, uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inserted
}

// waitChecked blocks until CheckDeferredBlock has returned (for the
// concurrent-dispatch tests, whose check goroutine may still be running
// when handlePushedBlock itself returns). This only bounds the mocked
// CheckDeferredBlock call itself; deferredCheck's own follow-up work (the
// stamp write, the notifier call) still runs after this returns, on the
// SAME goroutine and with no further blocking I/O, so waitFor below (not
// this) is what tests should use to observe the notifier's own state.
func (c *orderTestChain) waitChecked(t *testing.T) {
	t.Helper()
	select {
	case <-c.checkDoneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("CheckDeferredBlock did not complete")
	}
}

// waitFor polls cond (cheap, side-effect-free) until it is true or the
// timeout elapses -- used to observe an async goroutine's (deferredCheck
// dispatched via `go`) eventual, already-scheduled effect on the notifier
// without a fixed sleep.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(time.Millisecond)
	}
}

func newOrderTestService(chain *orderTestChain, notifier *recordingNotifier) *Service {
	return &Service{ctx: context.Background(), cfg: &config{chain: chain, blockImportNotifier: notifier}}
}

func orderTestBlock(number uint64, parent types.Hash) *block.Block {
	blk := block.NewBlock(&block.Header{
		ParentHash: parent,
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(0),
	}, nil)
	return blk.(*block.Block)
}

// Switch off (concurrent=false): InsertChain must not start until
// CheckDeferredBlock has fully returned -- exactly today's order.
func TestHandlePushedBlockSequentialRunsCheckBeforeInsert(t *testing.T) {
	chain := newOrderTestChain()
	chain.checkDelay = 50 * time.Millisecond
	notifier := &recordingNotifier{}
	svc := newOrderTestService(chain, notifier)
	blk := orderTestBlock(1, types.Hash{0x01})

	svc.handlePushedBlock(blk, false)

	chain.mu.Lock()
	defer chain.mu.Unlock()
	if chain.insertStart.Before(chain.checkDone) {
		t.Fatalf("sequential: InsertChain started (%v) before the check finished (%v)", chain.insertStart, chain.checkDone)
	}
}

// Switch on (concurrent=true): InsertChain must be dispatched immediately,
// not wait for the (slow) check.
func TestHandlePushedBlockConcurrentDispatchesInsertImmediately(t *testing.T) {
	chain := newOrderTestChain()
	chain.checkDelay = 100 * time.Millisecond
	notifier := &recordingNotifier{}
	svc := newOrderTestService(chain, notifier)
	blk := orderTestBlock(2, types.Hash{0x02})

	svc.handlePushedBlock(blk, true)
	chain.waitChecked(t)

	chain.mu.Lock()
	defer chain.mu.Unlock()
	if !chain.insertStart.Before(chain.checkDone) {
		t.Fatalf("concurrent: InsertChain started (%v) after the slow check finished (%v); it should overlap", chain.insertStart, chain.checkDone)
	}
}

// Both the check and the import pass: both notifications fire, regardless
// of which finished first.
func TestHandlePushedBlockBothPass(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		chain := newOrderTestChain()
		notifier := &recordingNotifier{}
		svc := newOrderTestService(chain, notifier)
		blk := orderTestBlock(3, types.Hash{0x03})

		svc.handlePushedBlock(blk, concurrent)
		chain.waitChecked(t)
		// deferredCheck's own follow-up work (stamp write, the notifier
		// call) runs after CheckDeferredBlock returns, on the same
		// goroutine but with no further blocking call -- wait for it to
		// actually reach NotifyBlockChecked rather than assuming
		// waitChecked (which only bounds the mocked call itself) is enough.
		waitFor(t, 2*time.Second, func() bool { return notifier.checkedCount() == 1 })

		notifier.mu.Lock()
		imported := notifier.imported
		notifier.mu.Unlock()
		if imported != 1 {
			t.Fatalf("concurrent=%v: imported notifications = %d, want 1", concurrent, imported)
		}
	}
}

// The check fails (e.g. a rule InsertChain's own executor does not
// enforce) but the import succeeds regardless: InsertChain's own outcome
// is untouched (no vote via the checked path, but the import-gated vote
// still fires via NotifyBlockImported) -- confirming PART 2's own
// "does not corrupt the chain, does not vote via the failed check" claim.
func TestHandlePushedBlockCheckFailsImportSucceeds(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		chain := newOrderTestChain()
		chain.checkErr = errors.New("block: blob transactions are not includable under deferred execution")
		notifier := &recordingNotifier{}
		svc := newOrderTestService(chain, notifier)
		blk := orderTestBlock(4, types.Hash{0x04})

		svc.handlePushedBlock(blk, concurrent)
		chain.waitChecked(t)

		if notifier.checkedCount() != 0 {
			t.Fatalf("concurrent=%v: a failed check must never notify NotifyBlockChecked, got %d", concurrent, notifier.checkedCount())
		}
		notifier.mu.Lock()
		imported := notifier.imported
		notifier.mu.Unlock()
		if imported != 1 {
			t.Fatalf("concurrent=%v: a failed check must not block the import's own vote; imported = %d, want 1", concurrent, imported)
		}
	}
}

// The check is slower than the import: the commit vote must advance from
// the import alone (NotifyBlockImported), without waiting for the check.
func TestHandlePushedBlockCheckSlowerThanImportVotesViaImportAlone(t *testing.T) {
	chain := newOrderTestChain()
	chain.checkDelay = 200 * time.Millisecond
	notifier := &recordingNotifier{}
	svc := newOrderTestService(chain, notifier)
	blk := orderTestBlock(5, types.Hash{0x05})

	start := time.Now()
	svc.handlePushedBlock(blk, true)
	elapsed := time.Since(start)

	notifier.mu.Lock()
	imported := notifier.imported
	notifier.mu.Unlock()
	if imported != 1 {
		t.Fatalf("imported notifications = %d, want 1", imported)
	}
	if elapsed >= chain.checkDelay {
		t.Fatalf("handlePushedBlock took %v, at least as long as the slow check (%v) -- it waited for the check instead of returning once the (fast) import finished", elapsed, chain.checkDelay)
	}
	chain.waitChecked(t) // let the background check finish before the test ends
}

// The import itself fails: no NotifyBlockImported, matching today's
// behaviour regardless of the switch.
func TestHandlePushedBlockImportFailureDoesNotNotify(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		chain := newOrderTestChain()
		chain.insertErr = errors.New("execution invalid")
		notifier := &recordingNotifier{}
		svc := newOrderTestService(chain, notifier)
		blk := orderTestBlock(6, types.Hash{0x06})

		svc.handlePushedBlock(blk, concurrent)
		chain.waitChecked(t)

		notifier.mu.Lock()
		imported := notifier.imported
		notifier.mu.Unlock()
		if imported != 0 {
			t.Fatalf("concurrent=%v: a failed import notified imported %d times, want 0", concurrent, imported)
		}
	}
}
