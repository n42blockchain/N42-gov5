// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

type arrivingTestFetcher struct {
	*executionTestFetcher
	arriving atomic.Bool
}

func (f *arrivingTestFetcher) BlockArriving(types.Hash) bool { return f.arriving.Load() }

func newArrivingTestService(t *testing.T) (*Service, *arrivingTestFetcher, types.Hash, *atomic.Bool) {
	t.Helper()
	svc, fetcher, hash := newExecutionTestService(t, false)
	fetcher.targetCh = make(chan uint64, 4)
	af := &arrivingTestFetcher{executionTestFetcher: fetcher}
	af.arriving.Store(true)
	svc.blockFetcher = af
	available := new(atomic.Bool)
	svc.SetAvailabilityProbe(func(h types.Hash) bool { return h == hash && available.Load() })
	return svc, af, hash, available
}

// The block passes its availability check after the commit: no catch-up, no
// failure on the production guard.
func TestCommitWhileArrivingDefersAndSkipsCatchUp(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	svc, af, hash, available := newArrivingTestService(t)
	_, avail0, cu0 := CommitArrivalStats()

	svc.observeCommitted(hash)
	if svc.unexecutedCommitFailures != 0 {
		t.Fatal("the commit was observed while its block was still arriving")
	}
	time.Sleep(5 * time.Millisecond)
	available.Store(true)
	af.arriving.Store(false)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, avail, _ := CommitArrivalStats(); avail > avail0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deferred observation never found the block available")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case n := <-af.targetCh:
		t.Fatalf("catch-up to %d requested for a block that arrived", n)
	case <-time.After(20 * time.Millisecond):
	}
	if _, _, cu := CommitArrivalStats(); cu != cu0 {
		t.Fatal("catch-up counted for an arrived block")
	}
	svc.pendingMu.Lock()
	failures := svc.unexecutedCommitFailures
	svc.pendingMu.Unlock()
	if failures != 0 {
		t.Fatalf("failures = %d, want 0", failures)
	}
}

// The block fails its availability check: it is observed and caught up as
// today, only later.
func TestCommitWhileArrivingStillCatchesUpAMissingBlock(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	svc, af, hash, _ := newArrivingTestService(t)

	svc.observeCommitted(hash)
	af.arriving.Store(false)
	select {
	case n := <-af.targetCh:
		if n != 42 {
			t.Fatalf("catch-up target = %d, want 42", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a block that never became available was not caught up")
	}
	svc.pendingMu.Lock()
	failures := svc.unexecutedCommitFailures
	svc.pendingMu.Unlock()
	if failures != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
}

// The wait is bounded: a block stuck in its push handler is caught up.
func TestCommitWhileArrivingIsBounded(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	prev := commitArrivalWait
	commitArrivalWait = 10 * time.Millisecond
	defer func() { commitArrivalWait = prev }()
	svc, af, hash, _ := newArrivingTestService(t)

	svc.observeCommitted(hash)
	select {
	case n := <-af.targetCh:
		if n != 42 {
			t.Fatalf("catch-up target = %d, want 42", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded wait never caught up")
	}
}

// Switch off: the commit is observed at once, as before.
func TestCommitWhileArrivingSwitchOffObservesAtOnce(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(false)()
	svc, af, hash, _ := newArrivingTestService(t)

	svc.observeCommitted(hash)
	if svc.unexecutedCommitFailures != 1 {
		t.Fatalf("failures = %d, want 1 (observed at once)", svc.unexecutedCommitFailures)
	}
	select {
	case n := <-af.targetCh:
		if n != 42 {
			t.Fatalf("catch-up target = %d, want 42", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no catch-up with the switch off")
	}
}
