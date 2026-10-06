// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S23 unit tests for async_write.go (docs/QS_BLOCK_TIME_BUDGET.md 6ct/6cu):
// ordered writes, back-pressure, shutdown drain, and the ExpectedParent
// bypass tracking. These construct asyncBlockWriter directly with a fake
// `process` function rather than going through newAsyncBlockWriter (which
// needs a real *worker calling the real writeAndFinish) -- process is the
// ONLY thing run() calls per job, so this exercises the writer's own FIFO/
// back-pressure/shutdown logic in full, independent of WriteBlockWithState.

package miner

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// fakeSealParentChecker is a minimal sealParentChecker test double: it
// always returns wantErr, and records whether it was called at all -- what
// checkSealParentApplied's bypass tests need to check is precisely whether
// the REAL check ran or was skipped.
type fakeSealParentChecker struct {
	wantErr error
	called  bool
}

func (f *fakeSealParentChecker) CheckSealParentApplied(block.IBlock) error {
	f.called = true
	return f.wantErr
}

// TestCheckSealParentAppliedBypassesWhenParentIsExpected: with the async
// writer reporting blk's parent as its own most-recently-accepted job, the
// real (DB-based) check must be skipped entirely -- this is the fix for
// U1's own gap (worker.go's checkSealParentApplied, async_write.go's
// pendingWrite doc comment): without it, a parent simply QUEUED for write
// reads identically to a genuinely stale one.
func TestCheckSealParentAppliedBypassesWhenParentIsExpected(t *testing.T) {
	parent := types.Hash{1, 2, 3}
	// Get a job "in flight" so ExpectedParent has something to report,
	// without racing the writer goroutine draining it immediately.
	release := make(chan struct{})
	started := make(chan struct{})
	aw := newTestAsyncWriter(func(*writeJob) {
		close(started)
		<-release
	})
	defer func() { close(release); aw.Drain(time.Second) }()
	aw.Enqueue(&writeJob{blockNumber: 1, hash: parent})
	<-started

	w := &worker{asyncWriter: aw}
	checker := &fakeSealParentChecker{wantErr: errors.New("would have been rejected")}
	if err := w.checkSealParentApplied(checker, nil, parent); err != nil {
		t.Fatalf("checkSealParentApplied = %v, want nil (bypassed)", err)
	}
	if checker.called {
		t.Fatal("the real CheckSealParentApplied ran despite the bypass condition matching")
	}
}

// TestCheckSealParentAppliedFallsThroughWhenParentDiffers checks the
// bypass does NOT fire for an unrelated parent (a genuine sibling/stale
// case must still go through the real check).
func TestCheckSealParentAppliedFallsThroughWhenParentDiffers(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	aw := newTestAsyncWriter(func(*writeJob) {
		close(started)
		<-release
	})
	defer func() { close(release); aw.Drain(time.Second) }()
	aw.Enqueue(&writeJob{blockNumber: 1, hash: types.Hash{9, 9}})
	<-started

	w := &worker{asyncWriter: aw}
	checker := &fakeSealParentChecker{wantErr: errors.New("genuinely stale")}
	otherParent := types.Hash{1, 1, 1}
	err := w.checkSealParentApplied(checker, nil, otherParent)
	if !checker.called {
		t.Fatal("the real CheckSealParentApplied did not run for a parent that does not match the pending job")
	}
	if err == nil {
		t.Fatal("checkSealParentApplied swallowed the real check's error")
	}
}

// TestCheckSealParentAppliedFallsThroughWhenSwitchOff checks the switch-off
// (asyncWriter nil) path always runs the real check -- byte-for-byte
// today's behaviour.
func TestCheckSealParentAppliedFallsThroughWhenSwitchOff(t *testing.T) {
	w := &worker{} // asyncWriter nil: switch off
	checker := &fakeSealParentChecker{wantErr: nil}
	if err := w.checkSealParentApplied(checker, nil, types.Hash{1}); err != nil {
		t.Fatalf("checkSealParentApplied = %v, want nil", err)
	}
	if !checker.called {
		t.Fatal("the real CheckSealParentApplied did not run with the switch off")
	}
}

func newTestAsyncWriter(process func(*writeJob)) *asyncBlockWriter {
	aw := &asyncBlockWriter{
		jobCh: make(chan *writeJob, 1),
		done:  make(chan struct{}),
	}
	aw.process = process
	go aw.run()
	return aw
}

func TestLeaderWriteAsyncDefaultsOff(t *testing.T) {
	if LeaderWriteAsyncOn() {
		t.Skip("N42_LEADER_WRITE_ASYNC=1 is set in this test binary's environment")
	}
}

// TestAsyncWriterOrdersJobsStrictly seals N jobs "concurrently" (fired from
// N goroutines at once) and checks the writer processes them in the exact
// order Enqueue was called -- the property S23's own safety argument
// (pendingWrite's doc comment) depends on: job N's true outcome must be
// settled before job N+1 is attempted.
func TestAsyncWriterOrdersJobsStrictly(t *testing.T) {
	var mu sync.Mutex
	var order []uint64
	aw := newTestAsyncWriter(func(job *writeJob) {
		mu.Lock()
		order = append(order, job.blockNumber)
		mu.Unlock()
	})

	const n = 20
	for i := uint64(1); i <= n; i++ {
		// Enqueue itself is called serially here (matching handleSealed's
		// own single-goroutine, serial-by-construction call pattern) -- the
		// ordering guarantee under test is the CHANNEL's, not concurrent
		// Enqueue calls racing each other (handleSealed never does that).
		aw.Enqueue(&writeJob{blockNumber: i, hash: types.Hash{byte(i)}})
	}
	if !aw.Drain(2 * time.Second) {
		t.Fatal("writer did not drain within 2s")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != n {
		t.Fatalf("processed %d jobs, want %d", len(order), n)
	}
	for i, num := range order {
		if num != uint64(i+1) {
			t.Fatalf("order[%d] = %d, want %d (jobs must process in seal order)", i, num, i+1)
		}
	}
}

// TestAsyncWriterEnqueueBlocksAtCapacityAndResumes: with a slow process
// function, a THIRD Enqueue call (1 in flight + 1 already queued) must
// block until the writer frees a slot, then succeed -- degrading to
// synchronous behaviour under back-pressure instead of growing memory,
// per the task's own requirement.
func TestAsyncWriterEnqueueBlocksAtCapacityAndResumes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan uint64, 8)
	aw := newTestAsyncWriter(func(job *writeJob) {
		started <- job.blockNumber
		<-release // held open until the test lets it go
	})

	aw.Enqueue(&writeJob{blockNumber: 1}) // becomes "in flight" (blocks on release)
	<-started                             // wait until it is actually being processed
	aw.Enqueue(&writeJob{blockNumber: 2}) // fills the one queue slot, does not block

	thirdDone := make(chan struct{})
	go func() {
		aw.Enqueue(&writeJob{blockNumber: 3}) // must block: capacity is 1 in flight + 1 queued
		close(thirdDone)
	}()

	select {
	case <-thirdDone:
		t.Fatal("third Enqueue returned before the writer freed a slot -- capacity bound not enforced")
	case <-time.After(100 * time.Millisecond):
	}

	close(release) // let job 1 finish
	select {
	case <-thirdDone:
	case <-time.After(2 * time.Second):
		t.Fatal("third Enqueue did not resume after the writer freed a slot")
	}
	// Job 2 is now "in flight" against the same release channel (already
	// closed), so it and job 3 drain immediately.
	if !aw.Drain(2 * time.Second) {
		t.Fatal("writer did not drain within 2s")
	}
}

// TestAsyncWriterEnqueueStampsWaitAndDepth checks wqWaitMs/wqDepth are only
// set when the enqueue call actually had to wait, matching "miner: seal
// path"'s own zero-means-not-applicable convention.
func TestAsyncWriterEnqueueStampsWaitAndDepth(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	aw := newTestAsyncWriter(func(job *writeJob) {
		started <- struct{}{}
		<-release
	})
	defer aw.Drain(2 * time.Second)

	fast := &writeJob{blockNumber: 1}
	aw.Enqueue(fast)
	if fast.wqWaitMs != 0 {
		t.Fatalf("fast.wqWaitMs = %d, want 0 (this enqueue did not block)", fast.wqWaitMs)
	}
	if fast.wqDepth != 0 {
		t.Fatalf("fast.wqDepth = %d, want 0 (queue was empty)", fast.wqDepth)
	}
	<-started

	queued := &writeJob{blockNumber: 2}
	aw.Enqueue(queued) // fills the queue slot, does not block, but depth=0 seen (slot was empty)
	if queued.wqWaitMs != 0 {
		t.Fatalf("queued.wqWaitMs = %d, want 0 (this enqueue found the one queue slot free)", queued.wqWaitMs)
	}

	blocked := &writeJob{blockNumber: 3}
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(release)
	}()
	aw.Enqueue(blocked)
	if blocked.wqWaitMs < 20 {
		t.Fatalf("blocked.wqWaitMs = %d, want >= ~20ms (release was delayed 30ms)", blocked.wqWaitMs)
	}
	if blocked.wqDepth != 1 {
		t.Fatalf("blocked.wqDepth = %d, want 1 (one job was already queued at enqueue time)", blocked.wqDepth)
	}
}

// TestAsyncWriterExpectedParentTracksThenClears checks ExpectedParent sees
// a job as soon as it is enqueued (before it is even processed) and stops
// seeing it once processing completes -- the exact timing CheckSealParentApplied's
// bypass (worker.go) depends on.
func TestAsyncWriterExpectedParentTracksThenClears(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	aw := newTestAsyncWriter(func(job *writeJob) {
		close(started)
		<-release
	})

	if _, _, ok := aw.ExpectedParent(); ok {
		t.Fatal("ExpectedParent found something before any job was enqueued")
	}

	h := types.Hash{9, 9}
	aw.Enqueue(&writeJob{blockNumber: 42, hash: h})
	<-started

	gotHash, gotNum, ok := aw.ExpectedParent()
	if !ok || gotHash != h || gotNum != 42 {
		t.Fatalf("ExpectedParent = (%x, %d, %v), want (%x, 42, true)", gotHash, gotNum, ok, h)
	}

	close(release)
	if !aw.Drain(2 * time.Second) {
		t.Fatal("writer did not drain within 2s")
	}
	if _, _, ok := aw.ExpectedParent(); ok {
		t.Fatal("ExpectedParent still reports the job after it finished processing")
	}
}

// TestAsyncWriterDrainWaitsForInFlightJob checks Drain does not return
// until a job already being processed has actually finished -- a shutdown
// must never leave a pushed/proposed block unwritten.
func TestAsyncWriterDrainWaitsForInFlightJob(t *testing.T) {
	var finished bool
	var mu sync.Mutex
	aw := newTestAsyncWriter(func(job *writeJob) {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		finished = true
		mu.Unlock()
	})
	aw.Enqueue(&writeJob{blockNumber: 1})
	if !aw.Drain(2 * time.Second) {
		t.Fatal("Drain timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	if !finished {
		t.Fatal("Drain returned before the in-flight job finished")
	}
}

// TestAsyncWriterDrainTimesOut checks Drain reports failure (rather than
// hanging forever) when the writer is genuinely stuck, so shutdown can log
// and proceed rather than block indefinitely.
func TestAsyncWriterDrainTimesOut(t *testing.T) {
	block := make(chan struct{})
	aw := newTestAsyncWriter(func(job *writeJob) {
		<-block // never released within this test
	})
	aw.Enqueue(&writeJob{blockNumber: 1})
	if aw.Drain(50 * time.Millisecond) {
		t.Fatal("Drain reported success against a writer that never finished")
	}
	close(block) // let the goroutine exit so the test does not leak it
	<-aw.done
}
