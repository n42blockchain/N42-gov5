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

package miner

import (
	"os"
	"strconv"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// EnvFrameHeldWaitMs (N42_FRAME_HELD_WAIT_MS, default 0 = off) bounds how
// long a build that stopped short at a held gateway waits for the frames it
// is held on (r90a).
//
// Why a build is held (E1N26 run 70): a gateway's frames are taken as an
// arrival-order prefix, and the prefix ends at the first frame whose
// senders are not cached (P10). Under frame attestation (P8b,
// N42_FRAME_ATTEST, committee k=1 on the bench) a node recovers only the
// frames its committee seat covers and waits for the others' attestations,
// so the newest frames of the stream are ready in a scattered order: in
// 28% of the B3 builds the leader stopped at such a frame with 652 or more
// fresh frames in its store (half of all loaded builds were held at all;
// p50 159 ready frames behind the held one). The
// existing remedy (N42_FRAME_FILL_HELD) hands the held frames to the
// recoverer's priority lane, but the recoverer deferred them again to the
// attestations (hurried p50 160 frames per held build, and still held), and
// the next build is another node's anyway.
//
// With the switch on, such a build -- held on at least one gateway and not
// stopped by the frame cap or the gas budget -- hands the held gateways'
// unready frames (arrival order, at most frameHurryCap) to this node's
// recoverer as local work (store.HurryLocal: recovered here unless their
// attestations arrive first), waits until the frame a gateway is held on
// has its senders, and picks again; it repeats while the pick is still
// held and short, for at most the configured milliseconds in all. Each pick
// is an ordinary pick: the block is whatever the switch-off rule takes from
// the store at that instant, so the per-gateway arrival-order prefix (and
// with it each sender's nonce order) is the same rule as without the
// switch. Nothing is recovered on the build goroutine (P10 holds).
const EnvFrameHeldWaitMs = "N42_FRAME_HELD_WAIT_MS"

var frameHeldWait = envFrameHeldWait()

func envFrameHeldWait() time.Duration {
	if v, err := strconv.Atoi(os.Getenv(EnvFrameHeldWaitMs)); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return 0
}

// SetFrameHeldWaitForTest sets N42_FRAME_HELD_WAIT_MS and returns the
// restore.
func SetFrameHeldWaitForTest(d time.Duration) func() {
	prev := frameHeldWait
	frameHeldWait = d
	return func() { frameHeldWait = prev }
}

// heldWaitPoll is how often a held build checks its head frames.
const heldWaitPoll = 200 * time.Microsecond

// heldRehurryEvery re-offers the head frames to local recovery while
// waiting: one offered while an attestation-deferred job held its claim is
// filtered out of HurryLocal and would otherwise wait for the deadline.
const heldRehurryEvery = time.Millisecond

// heldWait is the outcome of a build's held wait (observability).
type heldWait struct {
	waited  time.Duration // spent waiting (0: no wait)
	repicks int           // picks after the first
	local   int           // frames handed to local recovery
	gained  int           // frames over the first pick
	// The first pick's head frame (the frame the first held gateway was
	// held on): its admission age, and whether its recovery was running or
	// claimed by a block's import at that time.
	headAge              time.Duration
	headRunning, claimed bool
	// headSeat: the committee seat whose attestation covers the head frame
	// (frame.SeatOf; -1: no head frame, or no attestation).
	headSeat int
	expired  bool // the wait ended at the deadline still held
}

// pickHeldWait runs pick and, under N42_FRAME_HELD_WAIT_MS, the bounded
// held wait described there.
func pickHeldWait(st *store.Store, pick func() framePick) framePick {
	first := pick()
	hw := heldWait{headSeat: -1}
	if len(first.heads) > 0 && st != nil {
		info := st.RecoverInfoOf(first.heads[0])
		hw.headAge, hw.headRunning, hw.claimed = info.Age, info.Running, info.Claimed
		hw.headSeat = frame.SeatOf(first.heads[0])
	}
	if frameHeldWait <= 0 || st == nil || !frameReadyPick || first.held == 0 || first.capped {
		first.wait = hw
		return first
	}
	p := first
	start := time.Now()
	deadline := start.Add(frameHeldWait)
	for {
		hw.local += st.HurryLocal(p.unready)
		lastHurry := time.Now()
		ready := anyHasSenders(st, p.heads)
		for !ready && time.Now().Before(deadline) {
			time.Sleep(heldWaitPoll)
			ready = anyHasSenders(st, p.heads)
			if !ready && time.Since(lastHurry) >= heldRehurryEvery {
				hw.local += st.HurryLocal(p.heads)
				lastHurry = time.Now()
			}
		}
		if !ready {
			hw.expired = true
			break
		}
		p = pick()
		hw.repicks++
		if p.held == 0 || p.capped {
			break
		}
		if !time.Now().Before(deadline) {
			hw.expired = true
			break
		}
	}
	hw.waited = time.Since(start)
	hw.gained = len(p.hashes) - len(first.hashes)
	p.wait = hw
	return p
}

// anyHasSenders reports whether any of hs has cached senders.
func anyHasSenders(st *store.Store, hs []types.Hash) bool {
	for _, h := range hs {
		if st.HasSenders(h) {
			return true
		}
	}
	return false
}
