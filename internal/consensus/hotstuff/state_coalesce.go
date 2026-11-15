// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// hsStateCoalesceOn is N42_HS_STATE_COALESCE (default off, r90a). It acts
// only where N42_HS_STATE_JOURNAL routes a steady-state persist to the vote
// journal store (steadyStateDB).
//
// In E1N26 run 70 B3 those persists raised the journal store's write
// transactions from 28 to 43 a second per node and its commit from 1.5 to
// 3.9 ms (p90 6 -> 15): every vote waits for its own journal transaction to
// begin and sync, so the vote path (proposal -> QC -> next view) became the
// last prerequisite of 43% of full proposals (15-18% before).
//
// With the switch on a steady-state persist bound for the journal does not
// open a transaction. Its snapshot is kept as the pending record (a newer
// one replaces it, merged with the monotonic rules of SaveConsensusState)
// and written by whichever comes first:
//
//   - the next vote journal write (JournalVote): the pending record is
//     merged into the vote's record in memory and written by the vote's own
//     transaction -- no extra transaction, no extra sync. The vote's record
//     is a snapshot taken under the engine lock when the vote is cast, after
//     the pending one was taken (the pending snapshot also needs that lock),
//     so the merge leaves the vote's record as it is; the vote write is the
//     same write at the same point, durable before the vote is released;
//   - an idle flush N42_HS_STATE_COALESCE_MS (default 200) after the record
//     became pending, in a journal transaction of its own, exactly as the
//     persist without the switch -- for a node that is not voting.
//
// Recovery. The record that is pending is at most one vote journal write or
// the idle interval old, and only its own progress (view, lock, committed
// QC since the last durable record) can be lost in a crash; a vote
// commitment never is, because every vote is in the journal before it is
// released, with the lock and view of its snapshot. Without the switch the
// same persist runs after the commit on the output or commit loop, so a
// crash before its transaction commits loses exactly the same progress:
// every state a restart can find with the switch on is one it can find
// without it (crash before the persist), and recoverState merges the chain
// database's record (the canonical commit's, written with the block) and
// the journal field by field at the highest view as before. The steady-state
// persist is itself rate-limited (every persistInterval views and after a
// deferred commit), so the restart path already restores records that are
// views old; a stale view, lock or committed QC there is caught up by the
// network (view sync, a higher QC), as after any crash. The shutdown persist
// goes to the chain database and covers the pending record.
var hsStateCoalesceOn = parseHSOptIn(os.Getenv("N42_HS_STATE_COALESCE"))

// hsStateCoalesceIdle is N42_HS_STATE_COALESCE_MS (default 200 ms).
var hsStateCoalesceIdle = func() time.Duration {
	if n, err := strconv.Atoi(os.Getenv("N42_HS_STATE_COALESCE_MS")); err == nil && n > 0 {
		return time.Duration(n) * time.Millisecond
	}
	return 200 * time.Millisecond
}()

// SetHSStateCoalesceForTest switches N42_HS_STATE_COALESCE (and, when idle
// > 0, its idle flush) and returns the restore.
func SetHSStateCoalesceForTest(on bool, idle time.Duration) func() {
	prev, prevIdle := hsStateCoalesceOn, hsStateCoalesceIdle
	hsStateCoalesceOn = on
	if idle > 0 {
		hsStateCoalesceIdle = idle
	}
	return func() { hsStateCoalesceOn, hsStateCoalesceIdle = prev, prevIdle }
}

// stateCoalescer holds the pending steady-state record.
type stateCoalescer struct {
	mu      sync.Mutex
	pending *ConsensusState
	view    ViewNumber // the pending hook's view (lastPersistedView once written)
	timer   *time.Timer
	stopped bool

	coalesced, piggybacked, flushed atomic.Uint64
}

// put makes st (hook view v) the pending record, merged with a pending one,
// and returns whether the idle flush must be armed.
func (c *stateCoalescer) put(st *ConsensusState, v ViewNumber) (arm bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		if m, err := mergeConsensusStates(st, c.pending); err == nil {
			st = m
		} else {
			// Cannot happen for snapshots of one engine; the newer one
			// wins and the write's own merge reports any conflict.
			log.Warn("hotstuff: coalesced state merge failed, keeping the newer snapshot", "err", err)
		}
		if c.view > v {
			v = c.view
		}
	}
	c.pending, c.view = st, v
	return c.timer == nil && !c.stopped
}

// take removes and returns the pending record.
func (c *stateCoalescer) take() (*ConsensusState, ViewNumber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, v := c.pending, c.view
	c.pending, c.view = nil, 0
	return st, v
}

// putBack returns a record whose write failed.
func (c *stateCoalescer) putBack(st *ConsensusState, v ViewNumber) bool {
	if st == nil {
		return false
	}
	return c.put(st, v)
}

func (c *stateCoalescer) has() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending != nil
}

// coalescePersist makes h's snapshot the pending record (persistState under
// N42_HS_STATE_COALESCE).
func (s *Service) coalescePersist(h *stateHook) {
	s.coalesce.coalesced.Add(1)
	if s.coalesce.put(h.state, h.view) {
		s.armCoalesceFlush()
	}
}

func (s *Service) armCoalesceFlush() {
	c := &s.coalesce
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil || c.stopped || c.pending == nil {
		return
	}
	c.timer = time.AfterFunc(hsStateCoalesceIdle, s.flushCoalesced)
}

// flushCoalesced writes the pending record in a journal transaction of its
// own (the idle flush).
func (s *Service) flushCoalesced() {
	c := &s.coalesce
	c.mu.Lock()
	c.timer = nil
	c.mu.Unlock()
	st, v := c.take()
	if st == nil || s.journalDB == nil {
		return
	}
	if s.ctx.Err() != nil {
		// Shutting down: the shutdown persist writes the chain database.
		return
	}
	if err := s.journalDB.Update(kv.WithRwTag(s.ctx, "hsStateJournal"), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		log.Warn("hotstuff: failed to persist coalesced state", "err", err)
		if s.ctx.Err() == nil && c.putBack(st, v) {
			s.armCoalesceFlush()
		}
		return
	}
	c.flushed.Add(1)
	s.raiseLastPersistedView(v)
}

// stopCoalesce stops the idle flush (Stop; the shutdown persist covers the
// pending record).
func (s *Service) stopCoalesce() {
	c := &s.coalesce
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// raiseLastPersistedView sets lastPersistedView to v unless it is higher.
func (s *Service) raiseLastPersistedView(v ViewNumber) {
	s.persistViewMu.Lock()
	if v > s.lastPersistedView {
		s.lastPersistedView = v
	}
	s.persistViewMu.Unlock()
}
