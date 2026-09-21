// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// deferredTestChain answers the includability check from a script: one result
// per call, the last repeating.
type deferredTestChain struct {
	common.IBlockChain
	mu      sync.Mutex
	results []deferredResult
	calls   int
}

type deferredResult struct {
	checked bool
	retry   bool
	err     error
}

func (c *deferredTestChain) CheckDeferredBlock(block.IBlock) (bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.results[min(c.calls, len(c.results)-1)]
	c.calls++
	return r.checked, r.retry, r.err
}

func (c *deferredTestChain) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type recordingNotifier struct {
	mu          sync.Mutex
	checked     [][2]types.Hash
	imported    int
	rejected    int
	headerKnown [][2]types.Hash
}

func (n *recordingNotifier) NotifyBlockImported(types.Hash, types.Hash) {
	n.mu.Lock()
	n.imported++
	n.mu.Unlock()
}

func (n *recordingNotifier) NotifyBlockChecked(hash, parent types.Hash) {
	n.mu.Lock()
	n.checked = append(n.checked, [2]types.Hash{hash, parent})
	n.mu.Unlock()
}

func (n *recordingNotifier) NotifyBlockRejected(types.Hash) {
	n.mu.Lock()
	n.rejected++
	n.mu.Unlock()
}

func (n *recordingNotifier) NotifyBlockHeaderKnown(hash, parent types.Hash, _ uint64) {
	n.mu.Lock()
	n.headerKnown = append(n.headerKnown, [2]types.Hash{hash, parent})
	n.mu.Unlock()
}

func (n *recordingNotifier) checkedCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.checked)
}

func newDeferredWiringService(chain *deferredTestChain, notifier *recordingNotifier) *Service {
	return &Service{ctx: context.Background(), cfg: &config{chain: chain, blockImportNotifier: notifier}}
}

// A passing check must reach the consensus engine: this is the event the
// Round-2 gate votes on under deferred execution.
func TestDeferredCheckNotifiesTheEngine(t *testing.T) {
	parent := types.Hash{0xA1}
	chain := &deferredTestChain{results: []deferredResult{{checked: true}}}
	notifier := &recordingNotifier{}
	svc := newDeferredWiringService(chain, notifier)
	blk := testBlock(11, parent)

	svc.deferredCheck(blk)

	if notifier.checkedCount() != 1 {
		t.Fatalf("NotifyBlockChecked calls = %d, want 1", notifier.checkedCount())
	}
	if got := notifier.checked[0]; got[0] != blk.Hash() || got[1] != parent {
		t.Fatalf("notified (%x, %x), want (%x, %x)", got[0], got[1], blk.Hash(), parent)
	}
	if notifier.rejected != 0 {
		t.Fatalf("rejected %d times", notifier.rejected)
	}
}

// A check that cannot run yet (the parent is not this node's applied head) is
// kept and run again when the parent lands -- not dropped, and not failed.
func TestDeferredCheckRetriesWhenTheParentLands(t *testing.T) {
	parent := types.Hash{0xA2}
	chain := &deferredTestChain{results: []deferredResult{
		{checked: true, retry: true, err: errors.New("parent is not the applied head yet")},
		{checked: true},
	}}
	notifier := &recordingNotifier{}
	svc := newDeferredWiringService(chain, notifier)
	blk := testBlock(12, parent)

	svc.deferredCheck(blk)
	if notifier.checkedCount() != 0 {
		t.Fatal("a retry must not notify the engine")
	}

	svc.retryDeferredChildren(parent)
	if notifier.checkedCount() != 1 {
		t.Fatalf("after the parent landed: %d notifications, want 1", notifier.checkedCount())
	}
	if chain.callCount() < 2 {
		t.Fatalf("the check ran %d times, want it re-run", chain.callCount())
	}
}

// A block that genuinely fails the check is not voted for: no notification,
// and the import-gated path still governs it.
func TestDeferredCheckFailureDoesNotNotify(t *testing.T) {
	chain := &deferredTestChain{results: []deferredResult{{checked: true, err: errors.New("sender nonce 0, state expects 4096")}}}
	notifier := &recordingNotifier{}
	svc := newDeferredWiringService(chain, notifier)

	svc.deferredCheck(testBlock(13, types.Hash{0xA3}))

	if notifier.checkedCount() != 0 {
		t.Fatalf("a failed check notified the engine %d times", notifier.checkedCount())
	}
}

// Before the fork the checker reports checked=false and nothing happens.
func TestDeferredCheckBeforeTheForkIsSilent(t *testing.T) {
	chain := &deferredTestChain{results: []deferredResult{{}}}
	notifier := &recordingNotifier{}
	svc := newDeferredWiringService(chain, notifier)

	svc.deferredCheck(testBlock(14, types.Hash{0xA4}))

	if notifier.checkedCount() != 0 || notifier.rejected != 0 {
		t.Fatalf("pre-fork block produced notifications: checked=%d rejected=%d", notifier.checkedCount(), notifier.rejected)
	}
}
