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

package internal

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// The early QMDB capture (r99a, design r96 option C, N42_LAG_QCAP_EARLY,
// default off).
//
// Run 89 B3 (node0, steady full blocks): the block write waited a mean
// 5.0 ms (p50 4.2, p90 10.1; >= 1 ms on 84% of blocks) for its own QMDB row
// capture (qCapWaitUs), which starts only after the hand-off joined the
// previous write (lagWriteHandOff's invariant comment). With the switch on
// the hand-off takes the block's undo record, root, journal inputs and
// mutation count and starts the capture BEFORE it joins write N-1: the rows
// go to the tree's second staged window (commitment.StartFlushToEarly,
// qmdb.FlushToSecond), and the join's CommitFlushed + eviction of N-1's
// window run right after the capture (QMDBRootComputer.JoinAfterCapture).
// The capture then overlaps write N-1 instead of write N.
//
// What does not change (each a checkable statement):
//
//   - Q1 rows: block N's captured rows are byte-identical and in the same
//     order as the lean capture's (lib/qmdb TestFlushToSecondSameRows,
//     commitment TestEarlyCaptureSameRows, internal
//     TestLagQCapEarlySameRows: every table of the pipelined chain equal
//     with the switch off, on and check).
//   - Q2 undo / journal / root / gen: taken from the tree at block N's
//     post-state, before anything else touches it (the join only commits
//     and evicts; neither changes values, the undo or the journal inputs);
//     the journal is still written from these inputs in N's transaction.
//   - Q3 per-block atomicity and durability: unchanged; one transaction
//     per block with the same rows; the capture is memory only.
//   - Q4 window order: CommitFlushed strictly oldest first; at most two
//     windows; eviction only below the adopted cursor (=check asserts the
//     second window starts at the adopted cursor and that the eviction
//     drops exactly the twigs the capture treated as evicted).
//   - Q5 failure: a failed write N-1 found by the join aborts both windows
//     and reloads the tree from the store (recoverLagWrite, whose
//     AbortFlushed waits for N's capture); the capture's rows are dropped
//     with the job that never existed, and the tree is exactly what
//     today's recovery leaves (TestLagQCapEarlyFailedPrevWrite). A failed
//     capture of N drops only N's window; N's write then fails on it and
//     recovers as today.
//   - Q6 readers: the capture holds the tree's lock exclusively, from the
//     hand-off's start instead of after the join; sections that wait for
//     it (the child's SetCold / root, lookups) see the tree after "capture
//     N; commit N-1; evict N-1", the state they saw before after "commit
//     N-1; evict N-1; capture N" (the same tree: neither order changes a
//     value or the resident set those sections read differently).
//
// "check" is "1" plus the commitment-level E1/E2 assertions at every join
// (commitment.EarlyCaptureViolations, a panic in a test binary).
const (
	lagQCapEarlyOff uint32 = iota
	lagQCapEarlyOn
	lagQCapEarlyCheck
)

var lagQCapEarlyMode atomic.Uint32

func init() { lagQCapEarlyMode.Store(parseLagQCapEarly(os.Getenv("N42_LAG_QCAP_EARLY"))) }

func parseLagQCapEarly(v string) uint32 {
	switch v {
	case "check", "CHECK":
		return lagQCapEarlyCheck
	}
	if parseOptIn(v) {
		return lagQCapEarlyOn
	}
	return lagQCapEarlyOff
}

// SetLagQCapEarlyForTest switches N42_LAG_QCAP_EARLY ("", "1" or "check")
// and returns the restore.
func SetLagQCapEarlyForTest(v string) func() {
	prev := lagQCapEarlyMode.Load()
	lagQCapEarlyMode.Store(parseLagQCapEarly(v))
	return func() { lagQCapEarlyMode.Store(prev) }
}

// lagEarlyCaptures counts the hand-offs whose capture started before the
// join (since start); lagEarlyTails the joins whose commit + eviction ran
// as the capture's tail.
var lagEarlyCaptures, lagEarlyTails atomic.Uint64

// LagQCapEarlyStats returns the early captures, the joins run as their
// tail, and the commitment-level check violations since start.
func LagQCapEarlyStats() (captures, tails, violations uint64) {
	return lagEarlyCaptures.Load(), lagEarlyTails.Load(), commitment.EarlyCaptureViolations()
}

// lagTakeHandoffTree takes from the live tree what the job needs of it at
// the block's post-state: the undo record, the root, the journal inputs
// (before a capture takes the tree's lock) and the mutation count.
func (bc *BlockChain) lagTakeHandoffTree(j *lagWriteJob) {
	rc := bc.qmdbRootComputer
	j.undo = rc.TakeUndo()
	if j.undo == nil {
		j.undo = &qmdb.BlockUndo{PrevNextSlot: rc.Tree().NextSlot()}
	}
	j.root = rc.Root()
	j.hist = bc.captureQMDBHistoryInputs()
	j.gen = rc.AppliedState().Gen
}

// joinLagWriteAfterCapture is joinLagWriteMode for an early capture c of
// the next block: a successful join hands its CommitFlushed and eviction to
// c (they run right after it); a failed one recovers as joinLagWrite (the
// recovery's AbortFlushed waits for c and drops both windows).
func (bc *BlockChain) joinLagWriteAfterCapture(c *commitment.FlushCapture) (time.Duration, bool, error) {
	j := bc.lagWrite.inflight
	if j == nil {
		return 0, false, nil
	}
	if fn := lagWriteJoinForTest; fn != nil {
		fn(j.number)
	}
	t0 := time.Now()
	<-j.done
	wait := time.Since(t0)
	if j.err != nil {
		if err := bc.recoverLagWrite(j); err != nil {
			return wait, false, err
		}
		bc.lagWrite.inflight = nil
		return wait, false, fmt.Errorf("lag writer: block %d not written: %w", j.number, j.err)
	}
	bc.lagWrite.inflight = nil
	if bc.qmdbRootComputer.JoinAfterCapture(c, true) {
		lagEarlyTails.Add(1)
	}
	return wait, true, nil
}
