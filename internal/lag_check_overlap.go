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
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// The availability check overlapped with the lag executor
// (N42_PUSH_CHECK_OVERLAP).
//
// A pushed block the lag executor owns is queued by its availability check
// (checkAvailability), which runs on the push handler before anything else
// (S148: 33-36 ms per block): its sender recovery, its preparation and its
// ahead run on the published parent all start only after the check, and a
// foreign block reaches its executed result ~60 ms after an own one.
//
// With the flag on the push handler queues the block unchecked at arrival
// (LagEnqueueUnchecked) and then runs the check as before. An unchecked
// entry starts the block's sender recovery and preparation and may run
// ahead on its parent's published snapshot -- execution only, on its own
// state -- but nothing else sees it: LagImportQueued reports false (the
// gossip path and the future queue behave as before), it is not remembered
// as available (no child's check finds it as a parent), and the executor
// does not import it (no root, write, result, hook or gate release), so no
// snapshot of it is published and no child runs ahead on it. The passing
// check queues the block as before (enqueueLagImportTimed), which marks the
// entry checked and kicks the executor; the import then takes the ahead
// run. A check that does not pass -- an error, a retry, a block already
// applied -- leaves the entry unchecked, and the handler removes it
// (LagDropUnchecked) with its sender job, preparation and ahead run, then
// continues on the path it takes today. Votes stay on the check's own
// NotifyBlockChecked.

// pushCheckOverlap is N42_PUSH_CHECK_OVERLAP (default off; "1" turns it
// on).
var pushCheckOverlap = os.Getenv("N42_PUSH_CHECK_OVERLAP") == "1"

// SetPushCheckOverlapForTest switches the overlap and returns the restore.
func SetPushCheckOverlapForTest(on bool) func() {
	prev := pushCheckOverlap
	pushCheckOverlap = on
	return func() { pushCheckOverlap = prev }
}

// Blocks queued unchecked, those whose check then passed, those dropped
// (the check did not pass), and the executor's total hold on passed ones
// (ms: from finding the block importable to the check passing)
// (process-wide, since start).
var overlapQueued, overlapPassed, overlapDropped, overlapHeldMs atomic.Uint64

// CheckOverlapStats returns the blocks queued unchecked, passed and dropped,
// and the executor's total hold in ms (since start).
func CheckOverlapStats() (queued, passed, dropped, heldMs uint64) {
	return overlapQueued.Load(), overlapPassed.Load(), overlapDropped.Load(), overlapHeldMs.Load()
}

// LagEnqueueUnchecked queues b for the lag executor before its availability
// check, when the overlap is on, the executor owns b and nothing queued it
// yet. It reports whether it did: the caller then runs the check and calls
// LagDropUnchecked.
func (bc *BlockChain) LagEnqueueUnchecked(b *block.Block) bool {
	if !pushCheckOverlap || b == nil || !bc.LagExecutorOwns(b) || !bc.lagPipeUsable() {
		return false
	}
	le := &bc.lagExec
	le.mu.Lock()
	if le.queued == nil {
		le.queued = make(map[types.Hash]*lagQueued)
		le.kick = make(chan struct{}, 1)
	}
	if _, ok := le.queued[b.Hash()]; ok {
		le.mu.Unlock()
		return false
	}
	q := &lagQueued{blk: b, since: time.Now()}
	q.unchecked.Store(true)
	le.queued[b.Hash()] = q
	le.mu.Unlock()
	overlapQueued.Add(1)
	bc.startAheadPrep(b)
	if le.started.CompareAndSwap(false, true) {
		go bc.lagLoop()
	}
	// Its parent may be published already: run ahead now.
	bc.lagPipeLaunch(lagLaunchEnqueue)
	return true
}

// LagDropUnchecked removes hash's entry if it is still unchecked (its check
// did not pass) with everything started for it, and reports whether it did.
func (bc *BlockChain) LagDropUnchecked(hash types.Hash) bool {
	le := &bc.lagExec
	le.mu.Lock()
	q := le.queued[hash]
	if q == nil || !q.unchecked.Load() {
		le.mu.Unlock()
		return false
	}
	delete(le.queued, hash)
	le.mu.Unlock()
	overlapDropped.Add(1)
	bc.dropSenderJob(hash)
	bc.dropAheadPrep(hash)
	// Its ahead run, the only result built on it (an unchecked block is
	// never imported, so nothing runs ahead on it).
	bc.lagPipeForget(hash)
	log.Info("lag executor: unchecked block dropped, its check did not pass", "number", q.blk.Number64().Uint64(),
		"hash", hash.Hex()[:12], "queuedMs", time.Since(q.since).Milliseconds())
	return true
}

// markCheckedLocked marks q checked (le.mu held) once its check passed with
// avail; the executor's hold on it is logged and counted.
func (q *lagQueued) markCheckedLocked(avail time.Duration) {
	if !q.unchecked.Load() {
		return
	}
	q.avail = avail
	q.unchecked.Store(false)
	overlapPassed.Add(1)
	var held time.Duration
	if ns := q.heldSince.Load(); ns != 0 {
		held = time.Since(time.Unix(0, ns))
		overlapHeldMs.Add(uint64(held.Milliseconds()))
	}
	log.Info("lag executor: overlapped check passed", "number", q.blk.Number64().Uint64(),
		"checkMs", avail.Milliseconds(), "queuedMs", time.Since(q.since).Milliseconds(),
		"held", ns2bool(q.heldSince.Load()), "heldMs", held.Milliseconds())
}

func ns2bool(ns int64) bool { return ns != 0 }
