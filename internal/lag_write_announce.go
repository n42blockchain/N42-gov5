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
	"testing"
	"time"

	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// The block write announced before it opens (r99a, design r96 option F,
// N42_LAG_WRITE_ANNOUNCE, default off).
//
// Run 89 B3 (node0, steady full blocks): 35% of the ordered block writes
// waited >= 1 ms to begin (mean 3.2 ms, p90 10 ms), all but a handful behind
// a history fold write transaction (holder historyFoldYield). The yielding
// fold (N42_HISTORY_BACKFILL_YIELD_WRITER / N42_HISTORY_FOLD_YIELD) begins
// its transactions only while lagBGGate is closed, i.e. while no block write
// is open or waiting -- but the gate opens only when the write is already
// due (writeBlockWithStateOpt), and a fold transaction that began a few
// milliseconds earlier still holds the writer for its puts and its synced
// commit (4-60 ms on the shared device).
//
// With the switch on, a lag import whose write will be handed to the
// ordered writer raises an announcement when its state root starts (the
// root compute hook), ~25 ms before the hand-off, and the write releases it
// once it entered the gate. While an announcement is up:
//
//   - a fold write transaction does not begin (it waits as it waits for an
//     open block write), and
//   - a fold transaction already open stops putting at its next key
//     boundary (at least one key's rows go in, as today) and commits.
//
// Nothing about the fold's own rows, atomicity or durability changes: the
// same rows in the same order, cut into transactions at other points, each
// committed exactly as before (synced unless N42_HISTORY_FOLD_YIELD's
// no-sync mode is on), the marker with the last rows. Nothing about the
// block write changes either; only when fold transactions begin moves.
//
// Bounded starvation. An announcement can defer the begin of one fold
// transaction by at most N42_LAG_WRITE_ANNOUNCE_MAX_DEFER_MS (default 100)
// beyond today's wait (after that the transaction begins as it does today,
// as soon as no block write is open); and while the fold is more than
// N42_LAG_WRITE_ANNOUNCE_MAX_LAG_BLOCKS (default 1024) blocks behind the
// head, announcements are ignored altogether, so the fold's lag is never
// worse than max(that bound, today's lag). The "history fold" line stamps
// annDeferUs (time deferred by announcements), annIgnored and lagBlocks.
//
// "check" is "1" plus an assertion that announcements never leak or nest:
// at most one is up at any time (one lag import at a time, under bc.lock),
// and every raised one is released (counted in lagAnnounceLeaks, a panic in
// a test binary).
const (
	lagAnnounceOff uint32 = iota
	lagAnnounceOn
	lagAnnounceCheck
)

var lagWriteAnnounceMode atomic.Uint32

func init() {
	lagWriteAnnounceMode.Store(parseLagAnnounce(os.Getenv("N42_LAG_WRITE_ANNOUNCE")))
}

func parseLagAnnounce(v string) uint32 {
	switch v {
	case "check", "CHECK":
		return lagAnnounceCheck
	}
	if parseOptIn(v) {
		return lagAnnounceOn
	}
	return lagAnnounceOff
}

// SetLagWriteAnnounceForTest switches N42_LAG_WRITE_ANNOUNCE ("", "1" or
// "check") and returns the restore.
func SetLagWriteAnnounceForTest(v string) func() {
	prev := lagWriteAnnounceMode.Load()
	lagWriteAnnounceMode.Store(parseLagAnnounce(v))
	return func() { lagWriteAnnounceMode.Store(prev) }
}

var (
	lagWriteAnnounceMaxDefer = envMillis("N42_LAG_WRITE_ANNOUNCE_MAX_DEFER_MS", 100*time.Millisecond)
	lagWriteAnnounceMaxLag   = envUint("N42_LAG_WRITE_ANNOUNCE_MAX_LAG_BLOCKS", 1024)
)

// lagAnnounceLeaks counts check-mode violations (nested or leaked
// announcements) since start; lagAnnounceRaised the announcements raised.
var lagAnnounceLeaks, lagAnnounceRaised atomic.Uint64

func lagAnnounceViolation(what string) {
	lagAnnounceLeaks.Add(1)
	if testing.Testing() {
		panic("lag write announcement: " + what)
	}
	log.Error("lag write announcement: invariant violated", "what", what)
}

// lagWriteAnnouncement is one lag import's announcement of its block write.
type lagWriteAnnouncement struct {
	// state: 0 armed, 1 raised, 2 released.
	state atomic.Uint32
	at    atomic.Int64 // unix ns of the raise
}

// raise puts the announcement up (once; not after a release).
func (a *lagWriteAnnouncement) raise() {
	if a == nil || !a.state.CompareAndSwap(0, 1) {
		return
	}
	a.at.Store(time.Now().UnixNano())
	lagAnnounceRaised.Add(1)
	if n := lagBGGate.announced.Add(1); n != 1 && lagWriteAnnounceMode.Load() == lagAnnounceCheck {
		lagAnnounceViolation("two announcements up at once")
	}
}

// release takes it down (idempotent; a never-raised one is only retired).
// It returns how long it was up (0: never raised or released before).
func (a *lagWriteAnnouncement) release() time.Duration {
	if a == nil {
		return 0
	}
	if a.state.CompareAndSwap(0, 2) {
		return 0
	}
	if !a.state.CompareAndSwap(1, 2) {
		return 0
	}
	if n := lagBGGate.announced.Add(-1); n < 0 && lagWriteAnnounceMode.Load() == lagAnnounceCheck {
		lagAnnounceViolation("released more announcements than raised")
	}
	return time.Since(time.Unix(0, a.at.Load()))
}

// lagWriteAnnounceArm arms the announcement of blk's write on ibs's root
// (bc.lock held, inside the lag import's execution) and keeps it as the
// import's current one, which the hand-off takes; nil when the switch is
// off or the write will not be handed off.
func (bc *BlockChain) lagWriteAnnounceArm(ibs *state.IntraBlockState, lag *lagInsert, usable bool) *lagWriteAnnouncement {
	if lagWriteAnnounceMode.Load() == lagAnnounceOff || lag == nil || ibs == nil || !usable {
		return nil
	}
	if prev := bc.lagAnnounceCur; prev != nil {
		if prev.release() > 0 && lagWriteAnnounceMode.Load() == lagAnnounceCheck {
			lagAnnounceViolation("an import's announcement outlived it")
		}
	}
	a := &lagWriteAnnouncement{}
	ibs.AddRootComputeHook(a.raise)
	bc.lagAnnounceCur = a
	return a
}

// lagWriteAnnounceTake hands the current import's announcement to its
// write (the hand-off; bc.lock held).
func (bc *BlockChain) lagWriteAnnounceTake() *lagWriteAnnouncement {
	a := bc.lagAnnounceCur
	bc.lagAnnounceCur = nil
	return a
}

// lagWriteAnnounceDrop takes down the current import's announcement when
// its write was not handed off (bc.lock held).
func (bc *BlockChain) lagWriteAnnounceDrop() {
	if a := bc.lagAnnounceCur; a != nil {
		bc.lagAnnounceCur = nil
		a.release()
	}
}

// LagWriteAnnounceStats returns the announcements raised and the
// check-mode violations since start.
func LagWriteAnnounceStats() (raised, leaks uint64) {
	return lagAnnounceRaised.Load(), lagAnnounceLeaks.Load()
}
