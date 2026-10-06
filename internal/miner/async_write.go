// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S23 (docs/QS_BLOCK_TIME_BUDGET.md 6ct/6cu): N42_LEADER_WRITE_ASYNC=1 moves
// the leader's own WriteBlockWithState call (and everything downstream of a
// successful write: pendingTasks cleanup, counters, the "miner: seal path"/
// "miner: propose phases"/"Successfully sealed" log lines, recordSealedOnParent,
// the ChainHighestBlock event) off resultLoop and onto one dedicated writer
// goroutine, so resultLoop can receive and push/propose the NEXT sealed
// result without waiting for THIS block's write to finish (6ct's own U1
// finding: resultCh is unbuffered with exactly one consumer, so today a
// block sealed while the previous one's write is still running cannot even
// be received, let alone pushed).
//
// Default (unset/0): every function here is unreachable -- handleSealed's
// existing inline code path is untouched, byte-for-byte.

package miner

import (
	"os"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

var (
	leaderWriteAsyncOnce sync.Once
	leaderWriteAsyncOn   bool
)

// LeaderWriteAsyncOn reports N42_LEADER_WRITE_ASYNC=1.
func LeaderWriteAsyncOn() bool {
	leaderWriteAsyncOnce.Do(func() {
		leaderWriteAsyncOn = os.Getenv("N42_LEADER_WRITE_ASYNC") == "1"
	})
	return leaderWriteAsyncOn
}

// writeJob carries everything handleSealed has already computed by the time
// it decides to write, so writeAndFinish can run identically whether it is
// called inline (switch off) or from the dedicated writer goroutine (switch
// on). Every field here is something handleSealed's existing code already
// produces before reaching WriteBlockWithState today -- nothing new is
// computed, only carried further.
type writeJob struct {
	blk         block.IBlock
	receipts    []*block.Receipt
	logs        []*block.Log
	task        *task
	sealhash    types.Hash
	hash        types.Hash
	parentHash  types.Hash
	blockNumber uint64

	sealStart          time.Time
	tHandleSealedEnter time.Time
	tCheckEnter        time.Time
	dCheck             time.Duration
	tCopyStart         time.Time
	dCopy              time.Duration
	tPush              time.Time
	dPush              time.Duration
	pushedEarly        bool
	tProposeEarly      time.Time
	dProposeEarly      time.Duration
	proposedEarly      bool

	// S23 own stamps, filled in by asyncBlockWriter.Enqueue -- zero on the
	// synchronous (switch-off) path, matching "miner: seal path"'s existing
	// zero-means-not-applicable convention (tMs/waitMs, seal_path_diag.go).
	enqueuedAt time.Time // when this job was handed to the writer (zero if written inline)
	wqWaitMs   int64     // how long the enqueue call itself blocked (0 if it did not)
	wqDepth    int64     // queue length observed at enqueue time (0 or 1; capacity is 1+1 in flight)
}

// pendingWrite is the small, fixed-size record asyncBlockWriter keeps for
// whatever it currently owns (in flight or queued) -- at most two, given the
// bounded capacity below. Read by CheckSealParentApplied's caller
// (worker.go) so a block whose parent is simply QUEUED for write (not yet
// visible in the DB's applied marker) is not wrongly treated as stale: today
// that check runs only after the PREVIOUS block's write has already
// returned (resultLoop is single and serial), so it never needed to know
// about "pending" -- moving the write off resultLoop introduces exactly
// that gap, and this is what closes it.
//
// A wrong OPTIMISTIC pass here costs nothing: the actual, authoritative
// parent check runs again inside writeBlockWithState under bc.lock
// (checkQMDBLeaderSealParent, internal/blockchain_write.go:130,234) against
// the REAL committed DB state, regardless of what this bypass believed. If
// the block this optimistic pass trusted never actually applies (its own
// write failed for any reason), the NEXT queued write's call into
// writeBlockWithState will see the true (unchanged) applied head there and
// correctly return ErrStaleSeal -- exactly the same path and the same
// handling ("Sealed block lost to a competing candidate; dropping",
// worker.go) that already exists for an ordinary sibling race today. Strict
// FIFO order (one channel, one reader goroutine, see asyncBlockWriter) is
// what makes this safe: job N is always fully attempted, and its true
// outcome committed or not, before job N+1's own write ever calls
// checkQMDBLeaderSealParent.
type pendingWrite struct {
	hash   types.Hash
	number uint64
}

// asyncBlockWriter runs WriteBlockWithState (and everything after it) for
// one leader on its own dedicated goroutine, draining an ordered FIFO
// bounded to one job in flight plus one queued (a Go channel of capacity 1
// gives exactly this: the goroutine's own in-progress job is "in flight",
// and the channel can hold one more before a second Enqueue call blocks).
// Blocks are written strictly in the order they were sealed -- one channel,
// one reader goroutine, no reordering possible.
type asyncBlockWriter struct {
	w       *worker
	jobCh   chan *writeJob
	done    chan struct{}   // closed when the goroutine returns (Drain waits on this)
	process func(*writeJob) // w.writeAndFinish in production; swappable in tests

	mu      sync.Mutex
	pending []pendingWrite // 0-2 entries, oldest first; see ExpectedParent

	warnMu     sync.Mutex
	lastWarnAt time.Time
}

// newAsyncBlockWriter creates and starts the writer goroutine. Called from
// newWorker only when LeaderWriteAsyncOn() -- a switched-off node never
// allocates the channel or starts the goroutine.
func newAsyncBlockWriter(w *worker) *asyncBlockWriter {
	aw := &asyncBlockWriter{
		w:     w,
		jobCh: make(chan *writeJob, 1),
		done:  make(chan struct{}),
	}
	aw.process = func(job *writeJob) { w.writeAndFinish(job) }
	go aw.run()
	return aw
}

// ExpectedParent reports the hash/number of the block this writer will make
// canonical NEXT, once its queue drains -- either the last job it has
// accepted (in flight or queued), or (ok=false) nothing pending, meaning the
// caller should fall back to the real DB-applied check. Cheap: a mutex and
// an at-most-2-element slice.
func (aw *asyncBlockWriter) ExpectedParent() (hash types.Hash, number uint64, ok bool) {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	if len(aw.pending) == 0 {
		return types.Hash{}, 0, false
	}
	last := aw.pending[len(aw.pending)-1]
	return last.hash, last.number, true
}

// Enqueue records job as accepted (so ExpectedParent sees it immediately --
// this must happen before the NEXT block's own seal-parent check can
// possibly run, which is guaranteed here since handleSealed itself is the
// only caller and runs serially on resultLoop) and hands it to the writer,
// blocking if one job is already in flight AND one is already queued
// (capacity 1+1) -- i.e. degrading to today's synchronous behaviour under
// sustained back-pressure instead of growing memory. Records
// wqWaitMs/wqDepth on job before handing it off, and logs a rate-limited
// warning (at most once every 5s) when the enqueue call actually had to
// wait.
func (aw *asyncBlockWriter) Enqueue(job *writeJob) {
	aw.mu.Lock()
	aw.pending = append(aw.pending, pendingWrite{hash: job.hash, number: job.blockNumber})
	aw.mu.Unlock()

	job.enqueuedAt = time.Now()
	job.wqDepth = int64(len(aw.jobCh))
	select {
	case aw.jobCh <- job:
		return
	default:
	}
	aw.warnRateLimited(job.blockNumber)
	tBlock := time.Now()
	aw.jobCh <- job
	job.wqWaitMs = time.Since(tBlock).Milliseconds()
}

func (aw *asyncBlockWriter) warnRateLimited(number uint64) {
	aw.warnMu.Lock()
	defer aw.warnMu.Unlock()
	if time.Since(aw.lastWarnAt) < 5*time.Second {
		return
	}
	aw.lastWarnAt = time.Now()
	log.Warn("miner: leader write queue full; blocking (falling back to synchronous timing for this block)",
		"number", number)
}

// run is the writer's own goroutine: one job at a time, strictly in
// arrival (seal) order (see pendingWrite's doc comment for why a job built
// on a parent whose earlier write failed is still safe to attempt here,
// not merely convenient). Exits (closing done) once jobCh is closed AND
// drained -- see Drain.
func (aw *asyncBlockWriter) run() {
	defer close(aw.done)
	for job := range aw.jobCh {
		aw.process(job)
		aw.mu.Lock()
		if len(aw.pending) > 0 && aw.pending[0].hash == job.hash {
			aw.pending = aw.pending[1:]
		}
		aw.mu.Unlock()
	}
}

// Drain closes the job channel (no more sends are permitted after this --
// the caller, worker.close(), must guarantee resultLoop has already
// stopped calling handleSealed) and waits for the writer goroutine to
// finish whatever is already in flight or queued, so a shutdown never
// leaves a pushed/proposed block unwritten. Bounded by timeout so a stuck
// write (e.g. a wedged MDBX transaction) cannot hang shutdown forever;
// returns false if the timeout elapsed first.
func (aw *asyncBlockWriter) Drain(timeout time.Duration) bool {
	close(aw.jobCh)
	select {
	case <-aw.done:
		return true
	case <-time.After(timeout):
		return false
	}
}
