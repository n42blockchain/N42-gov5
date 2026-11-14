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
	"os"
	"time"
)

// lagWriteTxnEarly is N42_LAG_WRITE_TXN_EARLY (default off, r87b).
//
// The ordered writer waited for the block's state preparation (and its Log
// rows) before it opened the block's MDBX transaction: the "prepWait" at
// the head of every write, 11.9 ms p50 / 12.7 mean over run 67 B2's full
// blocks, because the preparation (~41 ms) starts only ~22 ms before the
// hand-off and the previous write is usually done by then. Inside the
// transaction ~16 ms (p50) of rows need neither preparation: the block and
// its evidence, the QMDB rows the hand-off captured, the undo record, the
// applied marker and the executed result.
//
// With the switch on the transaction opens at once and writes, in this
// order: the parent's total difficulty, the block, the evidence, the QMDB
// rows and markers, the receipts (waiting for their Log rows), then -- once
// the preparation is done -- the state and the changesets, the rewards, and
// the frames' wait, as before. The two groups write disjoint tables
// (state: PlainState/Code/Storage, the changesets; the rest: receipts, logs,
// block, evidence, the QMDB tables and markers, the executed result), so the
// committed transaction holds the same rows as the default order; it is
// still one transaction, committed once, so what a crash leaves is
// unchanged: the last committed block, never a part of one.
//
// What changes: the writer lock is taken before the preparation is done,
// and held for at most max(0, prepWait - those rows) longer than today.
// The executed result's root comes from the hand-off's read of the block's
// root (lagWriteJob.execRoot) instead of ibs, which the preparation may
// still own; it is the same value (the root computed for the block).
var lagWriteTxnEarly = os.Getenv("N42_LAG_WRITE_TXN_EARLY") == "1"

// lagWriteTxnEarlyMidForTest, when set, runs inside an early transaction
// after the rows that need no preparation and before the state's; an
// error aborts the transaction there (tests: a crash or a failure with
// part of the block's rows already put).
var lagWriteTxnEarlyMidForTest func(number uint64) error

// SetLagWriteTxnEarlyForTest switches the early transaction and returns the
// restore.
func SetLagWriteTxnEarlyForTest(on bool) func() {
	prev := lagWriteTxnEarly
	lagWriteTxnEarly = on
	return func() { lagWriteTxnEarly = prev }
}

// lagWritePhase is a step of one ordered write, stamped as it is entered
// (observability: what a hand-off's wait for the previous write sat in).
type lagWritePhase int

const (
	lwpStart       lagWritePhase = iota // job started (test hook, setup)
	lwpPrepWait                         // waiting for the preparations before the transaction
	lwpBegin                            // waiting for the MDBX writer (BeginRw)
	lwpBody                             // in the transaction: rows other than those below
	lwpPrepWaitTxn                      // in the transaction: waiting for the state preparation (TXN_EARLY)
	lwpChgset                           // changesets
	lwpQFlush                           // QMDB rows and markers
	lwpFramesWait                       // waiting for the frame store write
	lwpCommit                           // MDBX commit
	lwpPost                             // after the commit, until the job is done
	lwpCount
)

var lagWritePhaseNames = [lwpCount]string{"hwStart", "hwPrepWait", "hwBegin", "hwBody", "hwPrepWaitTxn",
	"hwChgset", "hwQflush", "hwFramesWait", "hwCommit", "hwPost"}

// mark stamps the entry into phase p (nil job: no-op). Only the writer
// goroutine writes the stamps; readers read them after the job's done
// channel closed.
func (j *lagWriteJob) mark(p lagWritePhase) {
	if j == nil {
		return
	}
	j.marks[p] = time.Now()
}

// waitSplit splits [from, j.end] over the phases the job went through:
// how long the wait for it sat in each phase (only after done closed).
func (j *lagWriteJob) waitSplit(from time.Time) [lwpCount]time.Duration {
	var out [lwpCount]time.Duration
	if j == nil || j.end.IsZero() || !from.Before(j.end) {
		return out
	}
	// The phases in the order they were entered.
	type seg struct {
		p  lagWritePhase
		at time.Time
	}
	segs := make([]seg, 0, lwpCount)
	for p := lagWritePhase(0); p < lwpCount; p++ {
		if !j.marks[p].IsZero() {
			segs = append(segs, seg{p, j.marks[p]})
		}
	}
	for i := 1; i < len(segs); i++ {
		for k := i; k > 0 && segs[k].at.Before(segs[k-1].at); k-- {
			segs[k], segs[k-1] = segs[k-1], segs[k]
		}
	}
	for i, s := range segs {
		end := j.end
		if i+1 < len(segs) {
			end = segs[i+1].at
		}
		lo := s.at
		if from.After(lo) {
			lo = from
		}
		if end.After(lo) {
			out[s.p] += end.Sub(lo)
		}
	}
	return out
}

// lagWaitSplitFields returns the last hand-off's wait for the previous
// write split by the phase that write was in (microseconds, nonzero ones).
func (bc *BlockChain) lagWaitSplitFields() []interface{} {
	sp := bc.lagWrite.lastWaitSplit.Load()
	if sp == nil {
		return nil
	}
	var fields []interface{}
	for p, d := range sp {
		if d > 0 {
			fields = append(fields, lagWritePhaseNames[p], d.Microseconds())
		}
	}
	return fields
}
