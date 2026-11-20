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

package qmdb

import (
	"errors"
	"slices"
)

// A second staged window (r99a, the lag writer's early capture,
// N42_LAG_QCAP_EARLY).
//
// Without it the ordered writer captures block N's rows only after write
// N-1 committed: CommitFlush(N-1) adopts N-1's cursor, the eviction of
// N-1's window (EvictLag.Evict: entries, then EvictTwigsThrough) drops the
// twigs that lie below it, and FlushTo(N) then writes from the adopted
// cursor. FlushToSecond writes the same rows while N-1's window is still
// staged:
//
//   - from: the caller passes N-1's staged cursor, which CommitFlush will
//     adopt (the same `from`).
//   - entries [from, nextSlot) and the dead-row deletes: functions of the
//     tree's state after N's root, which neither the commit nor the
//     eviction of N-1 changes (entry eviction only drops slots below the
//     cursor; deaths were classified when N's root ran, before either).
//     The deletes are staged in this window's own list (stagedNext.dead),
//     never mixed with N-1's (stagedDead).
//   - twig rows: the twigs N-1's eviction will drop (EvictTwigsThrough's
//     rule at evictThrough, selected exactly as it selects them, after the
//     same foldTouched) are written as that eviction leaves them: a meta row
//     only when metaDirty, no leaf blob. Every other twig as FlushTo.
//   - meta rows: the same root, nextSlot and live count.
//
// CommitFlush commits the first window and promotes the second to first;
// AbortFlush re-queues and drops both (a failed write N-1 reloads the tree
// anyway); AbortFlushNewest drops only the second (a failed capture of N
// while N-1 may still commit). A plain FlushTo while a second window is
// staged is refused (errSecondWindowStaged): three windows never exist.

// stagedWindow is the second staged window.
type stagedWindow struct {
	valid   bool
	through uint64
	dead    []uint64
}

func (w *stagedWindow) reset() {
	w.valid = false
	w.through = 0
	w.dead = w.dead[:0]
}

// errSecondWindowStaged: a flush while two windows are staged.
var errSecondWindowStaged = errors.New("qmdb: a second flush window is already staged")

// secondFlush is one FlushToSecond's eviction emulation.
type secondFlush struct {
	evictThrough uint64
	ids          []int // ascending: the twigs the eviction will drop
}

// selectEvicted chooses, as EvictTwigsThrough(evictThrough) would now, the
// twigs it drops: nothing without a cold reader; else, after foldTouched,
// every twig below the watermark that is resident, not pruned and clean.
func (s *secondFlush) selectEvicted(t *Tree) {
	s.ids = s.ids[:0]
	if t.cold == nil {
		return
	}
	t.foldTouched()
	s.ids = t.evictCandidates(s.evictThrough, s.ids)
}

// evictCandidates appends, ascending, the ids EvictTwigsThrough(through)
// would drop now (no mutation besides the twig set's compaction).
func (t *Tree) evictCandidates(through uint64, out []int) []int {
	maxID := int(through / TwigSize)
	if maxID > len(t.twigs) {
		maxID = len(t.twigs)
	}
	if use, _ := t.useTwigSet(FlushTwigSetMode()); use {
		t.compactTwigSet()
		for _, id := range t.tset.ids {
			if id >= maxID {
				break
			}
			if evictDropsTwig(t.twigs[id]) {
				out = append(out, id)
			}
		}
		return out
	}
	for id := 0; id < maxID; id++ {
		if evictDropsTwig(t.twigs[id]) {
			out = append(out, id)
		}
	}
	return out
}

// drops reports whether twig id is one the eviction drops.
func (s *secondFlush) drops(id int) bool {
	_, ok := slices.BinarySearch(s.ids, id)
	return ok
}

// FlushToSecond is FlushTo staged as the second window while the first
// (the previous block's, through `from`) is still uncommitted, writing the
// twig rows as they are after the first window's eviction at evictThrough
// (see the file comment). It requires a staged first window and no second
// one. It returns the new cursor, the bytes written and the ids of the
// twigs it treated as evicted (ascending; the caller may compare them with
// the eviction's own choice, EvictCandidates, when it runs).
func (t *Tree) FlushToSecond(p Putter, from, evictThrough uint64) (uint64, int, []int, error) {
	if t.stagedNext.valid {
		return from, 0, nil, errSecondWindowStaged
	}
	if !t.stagedFlushValid {
		return from, 0, nil, errors.New("qmdb: a second flush window needs a staged first one")
	}
	sec := &secondFlush{evictThrough: evictThrough}
	next, n, err := t.flushTo(p, from, sec)
	return next, n, sec.ids, err
}

// promoteSecondWindow makes the second window the first (CommitFlush).
func (t *Tree) promoteSecondWindow() {
	if !t.stagedNext.valid {
		return
	}
	t.stagedFlushThrough, t.stagedFlushValid = t.stagedNext.through, true
	t.stagedDead = append(t.stagedDead, t.stagedNext.dead...)
	t.stagedNext.reset()
}

// AbortFlushNewest drops only the newest staged window after its capture
// failed: the second when there is one (its deletes re-queued), else the
// first (AbortFlush).
func (t *Tree) AbortFlushNewest() {
	if !t.stagedNext.valid {
		t.AbortFlush()
		return
	}
	t.deadFlushed = append(t.deadFlushed, t.stagedNext.dead...)
	t.stagedNext.reset()
}

// SecondWindowStaged reports whether a second window is staged.
func (t *Tree) SecondWindowStaged() bool { return t.stagedNext.valid }

// EvictCandidates is, for checks, the ids EvictTwigsThrough(through) would
// drop now: nothing without a cold reader; else after foldTouched (which
// the eviction runs first anyway).
func (t *Tree) EvictCandidates(through uint64) []int {
	if t.cold == nil {
		return nil
	}
	t.foldTouched()
	return t.evictCandidates(through, nil)
}
