// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package commitment

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
)

// The early capture (r99a, design r96 option C; the lag writer's
// N42_LAG_QCAP_EARLY).
//
// StartFlushTo (N42_LAG_HANDOFF_LEAN) captures block N's rows after write
// N-1 was joined: CommitFlushed(N-1), then on the capture goroutine the
// eviction of N-1's window and FlushTo(N). StartFlushToEarly captures them
// at the hand-off's start, while write N-1 may still be in flight, into a
// second staged window (qmdb.Tree.FlushToSecond, persist_second.go: same
// `from`, same entry and dead rows, twig rows as after N-1's eviction).
// The join of N-1 then hands its CommitFlushed + eviction to the capture
// (JoinAfterCapture): they run at the end of the capture's exclusive hold
// when the capture is still running, or as their own exclusive sections
// when it already finished -- in both cases after the capture and before
// any other writer-side section that waited for the lock, i.e. the tree
// goes through "capture N; commit N-1; evict N-1" as one ordered sequence.
//
// Invariants (checkable; =check asserts the first two at every join):
//
//   - E1 contiguity: the second window starts at the first window's end,
//     the cursor CommitFlushed(N-1) adopts (from == flushedThrough after
//     the commit).
//   - E2 eviction: the twigs the capture wrote as evicted are exactly the
//     twigs N-1's eviction drops (qmdb.Tree.EvictCandidates at the real
//     eviction == the capture's choice).
//   - E3 rows: the captured rows equal the serial capture's, row for row
//     (lib/qmdb TestFlushToSecondSameRows; internal differential tests).
//   - E4 order: CommitFlushed strictly in window order; at most two
//     windows; eviction only below the adopted cursor (it runs after the
//     commit, as before).
//   - E5 failure: a failed write N-1 aborts both windows (AbortFlushed) and
//     the caller reloads the tree, exactly as today's recovery; a failed
//     capture of N drops only N's window (AbortFlushNewest), N-1's stays
//     and commits if its write does.
//
// earlyCaptureViolations counts =check failures of E1/E2 (since start).
var earlyCaptureViolations atomic.Uint64

// EarlyCaptureViolations reports how many =check failures were found.
func EarlyCaptureViolations() uint64 { return earlyCaptureViolations.Load() }

func earlyCaptureViolation(what string, args ...any) {
	earlyCaptureViolations.Add(1)
	if testing.Testing() {
		panic("QMDB early capture: " + what)
	}
	log.Error("QMDB early capture: invariant violated", append([]any{"what", what}, args...)...)
}

// earlyTail is a join's commit + eviction handed to an early capture.
type earlyTail struct {
	evict bool
}

// StartFlushToEarly is StartFlushTo for the lag writer's early capture: it
// may run while the previous block's window is staged and uncommitted, in
// which case the rows go to a second window (see the file comment). check
// arms the E1/E2 assertions at the join. A capture while two windows are
// staged fails (and so does one on a computer whose rows are not all in the
// flush: an MDBX-backed index or a history store).
func (r *QMDBRootComputer) StartFlushToEarly(p qmdb.Putter, check bool) *FlushCapture {
	c := &FlushCapture{done: make(chan struct{}), early: true, check: check}
	unlock := r.lockWriter(holderFlushEarly)
	go func() {
		t0 := time.Now()
		c.n, c.err = r.flushToEarlyLocked(p, c)
		c.st.Flush = r.t.LastFlushStats()
		c.st.Hist = r.lastFlushHist
		if c.err != nil {
			r.abortNewestLocked()
		}
		c.took = time.Since(t0)
		c.mu.Lock()
		tail := c.tail
		c.finished = true
		c.mu.Unlock()
		if tail != nil {
			r.runEarlyTailLocked(c, tail)
		}
		unlock()
		close(c.done)
	}()
	return c
}

// EarlyCaptureSupported reports whether StartFlushToEarly can capture this
// computer (every row it persists goes through the flush: no MDBX-backed
// index, no history store). The lag writer falls back to the lean capture
// otherwise.
func (r *QMDBRootComputer) EarlyCaptureSupported() bool {
	return r.mdbxIdx == nil && r.histStore == nil
}

func (r *QMDBRootComputer) flushToEarlyLocked(p qmdb.Putter, c *FlushCapture) (int, error) {
	if !r.EarlyCaptureSupported() {
		return 0, errors.New("qmdb: early capture of a computer that persists outside the flush")
	}
	if r.stagedNextValid {
		return 0, errors.New("qmdb: early capture with two windows staged")
	}
	tHist := time.Now()
	err := r.t.FlushHistory()
	r.lastFlushHist = time.Since(tHist)
	if err != nil {
		return 0, err
	}
	if !r.stagedValid {
		// Nothing in flight: the plain capture.
		next, n, err := r.t.FlushTo(p, r.flushedThrough)
		if err != nil {
			return n, err
		}
		r.stagedFlushed, r.stagedValid = next, true
		return n, nil
	}
	from := r.stagedFlushed
	evictThrough := uint64(0)
	if from > r.retainedEntries {
		evictThrough = from - r.retainedEntries
	}
	next, n, ids, err := r.t.FlushToSecond(p, from, evictThrough)
	if err != nil {
		return n, err
	}
	r.stagedNext, r.stagedNextValid = next, true
	c.second, c.from, c.emul = true, from, ids
	return n, nil
}

// abortNewestLocked drops the newest staged window (a failed capture).
func (r *QMDBRootComputer) abortNewestLocked() {
	if r.stagedNextValid {
		r.stagedNextValid = false
		r.t.AbortFlushNewest()
		return
	}
	r.abortFlushedLocked()
}

// JoinAfterCapture runs a successful join's CommitFlushed (and, with
// evict, EvictFlushed) of the previous window right after the early
// capture c: as the tail of c's exclusive hold when c is still running
// (async true; c.Done closes after it), else now, as their own exclusive
// sections.
func (r *QMDBRootComputer) JoinAfterCapture(c *FlushCapture, evict bool) (async bool) {
	tail := &earlyTail{evict: evict}
	c.mu.Lock()
	if !c.finished {
		c.tail = tail
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	unlock := r.lockWriter(holderCommitFlushed)
	r.runEarlyTailLocked(c, tail)
	unlock()
	return false
}

// runEarlyTailLocked commits the oldest window and evicts it, checking E1
// and E2 when c was armed for it.
func (r *QMDBRootComputer) runEarlyTailLocked(c *FlushCapture, tail *earlyTail) {
	t0 := time.Now()
	r.commitFlushedLocked()
	if c.check && c.second && c.err == nil && r.flushedThrough != c.from {
		earlyCaptureViolation("the second window does not start at the adopted cursor",
			"from", c.from, "adopted", r.flushedThrough)
	}
	if tail.evict {
		through := r.flushedThrough
		if through > r.retainedEntries {
			through -= r.retainedEntries
		} else {
			through = 0
		}
		if c.check && c.second && c.err == nil {
			if got := r.t.EvictCandidates(through); !slices.Equal(got, c.emul) {
				earlyCaptureViolation("the eviction drops other twigs than the capture assumed",
					"evicts", len(got), "assumed", len(c.emul))
			}
		}
		r.evictLaggedLocked(r.flushedThrough)
		c.st.EvictTwig = r.t.LastEvictStats()
		c.st.Evicted = true
	}
	c.st.Evict = time.Since(t0)
	c.st.Tail = true
}
