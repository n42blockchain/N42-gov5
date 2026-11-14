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
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/envswitch"
)

// Background writers yield the MDBX writer to the lag writer (r58a).
//
// The lag writer's "begin" -- the wait for the single MDBX writer -- was a
// median 0 but a mean 18 ms with spikes to 400 ms. The write probe over a
// full E1N26 leg (r19 B4, node0) named the holder: of the 699 block writes
// that waited 30 ms or more, 671 (38 s of the 43 s total wait) waited
// behind a history fold write transaction (AccountHistory/StorageHistory,
// held a median 63 ms, up to 214 ms: 30 ms of puts and the commit that
// writes the pages they dirtied). The rest overlapped only writes of the
// separate frame store, i.e. no ChainDB holder.
//
// Under load the lag writer is never idle for long: each write's
// preparation overlaps the previous write, so the next transaction begins
// right after the last commits, and a fold transaction started at any time
// is almost always in the way of one. With N42_LAG_WRITE_DEFER_BG=1 the
// history backfiller therefore
//
//   - does not start a fold while the lag writer was active within the last
//     N42_LAG_WRITE_DEFER_BG_IDLE_MS (default 1000) and the index is fewer
//     than N42_LAG_WRITE_DEFER_BG_MAX_BLOCKS (default: the fold's batch,
//     256) blocks behind. Folding a full batch at once also unions each hot
//     key's blocks into one row update instead of one per small fold; and
//   - starts each of a fold's write transactions only while no lag write
//     transaction is open (waiting at most N42_LAG_WRITE_DEFER_BG_WAIT_MS,
//     default 250, per transaction), so it lands in the gap after a commit.
//
// The fold is deferred, never dropped: it runs as soon as the chain is
// quiet, and at the latest once a full batch is pending. Historical queries
// above the index marker stay refused meanwhile, as they are for any fold
// in progress.
var (
	lagWriteDeferBG          = envswitch.Is1("N42_LAG_WRITE_DEFER_BG", envswitch.Perf, "Background writers yield the MDBX writer to the lag writer (r58a).")
	lagWriteDeferBGIdle      = envswitch.Millis("N42_LAG_WRITE_DEFER_BG_IDLE_MS", time.Second, envswitch.Perf, "Idle time after which background writers resume, in ms.")
	lagWriteDeferBGWait      = envswitch.Millis("N42_LAG_WRITE_DEFER_BG_WAIT_MS", 250*time.Millisecond, envswitch.Perf, "Longest a background writer waits for the lag writer, in ms.")
	lagWriteDeferBGMaxBlocks = envswitch.Uint64("N42_LAG_WRITE_DEFER_BG_MAX_BLOCKS", 0, envswitch.Perf, "Blocks a background writer may be deferred (0 = unlimited).")
)

// SetLagWriteDeferBGForTest switches the deferral and returns the restore.
func SetLagWriteDeferBGForTest(on bool) func() {
	prev := lagWriteDeferBG
	lagWriteDeferBG = on
	return func() { lagWriteDeferBG = prev }
}

// lagWriteBGGate tracks the lag writer's open write transactions.
type lagWriteBGGate struct {
	open       atomic.Int32
	lastActive atomic.Int64 // unix nanos of the last enter or exit
}

var lagBGGate lagWriteBGGate

// enter marks a lag write transaction about to begin (before BeginTxn, so
// a background writer sees the lag writer waiting too).
func (g *lagWriteBGGate) enter() {
	g.open.Add(1)
	g.lastActive.Store(time.Now().UnixNano())
}

// exit marks it committed or rolled back.
func (g *lagWriteBGGate) exit() {
	g.lastActive.Store(time.Now().UnixNano())
	g.open.Add(-1)
}

// activeWithin reports whether a lag write transaction is open or was
// within d.
func (g *lagWriteBGGate) activeWithin(d time.Duration) bool {
	if g.open.Load() > 0 {
		return true
	}
	last := g.lastActive.Load()
	return last != 0 && time.Since(time.Unix(0, last)) < d
}

// waitClosed waits until no lag write transaction is open, at most max or
// until done is closed. It reports how long it waited.
func (g *lagWriteBGGate) waitClosed(max time.Duration, done <-chan struct{}) time.Duration {
	t0 := time.Now()
	for g.open.Load() > 0 {
		if time.Since(t0) >= max {
			break
		}
		select {
		case <-done:
			return time.Since(t0)
		case <-time.After(500 * time.Microsecond):
		}
	}
	return time.Since(t0)
}
