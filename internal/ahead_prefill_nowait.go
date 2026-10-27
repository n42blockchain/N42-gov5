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
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// The ahead run's claim without the prefill wait (N42_AHEAD_PREFILL_NOWAIT).
//
// An ahead run claims its block's preparation and, by default, waits for
// the base prefill (ahead_base_prefill.go) to finish. When the preparation
// completes after the parent's snapshot hook -- the parent's ComputeRoot
// already labelled the tree with the parent, so the fill reads at depth 0
// -- every lookup of the fill waits behind that root's exclusive lock
// (ApplyOps), and the claim waits for the root and then for the whole fill
// (S148 node3: 1026 of 2944 163k blocks, claim wait 21.7 ms mean, the full
// fill; aheadPre p90 40 ms).
//
// With the flag on, a claim that finds the fill still waiting for the tree
// or still reading does not wait: the run builds its readers on a prefill
// slot (commitment.PrefillSlot) and starts; its lookups read through the
// tree (and its lock) as with no prefill until the fill finishes, passes its
// own bracket check (AppliedSince) and lands on the slot, after which the
// prefilled keys answer from it. A value read at tree state h is the child's
// base value for every key no block in (h, N] changed; the run's layers
// answer the rest (P15/P15b). The claim fixes the layers before the fill
// knows h, so a landing prefill is used only when h is a block of the
// child's chain at or above the lowest height the layers cover: the parent
// or its parent (layers as they are); with N42_AHEAD_BASE_DEPTH >= 2 also
// deeper, when the claim found the kept post-states of every block down to
// there (it then layers them, valid over any tree state from there to N).
// Otherwise the prefill is dropped and counted as a fallback: the run keeps
// reading per lookup. A fill that finished before the claim is used exactly
// as without the flag.

// aheadPrefillNoWait is N42_AHEAD_PREFILL_NOWAIT (default off; "1" turns
// it on).
var aheadPrefillNoWait = os.Getenv("N42_AHEAD_PREFILL_NOWAIT") == "1"

// SetAheadPrefillNoWaitForTest switches the no-wait claim and returns the
// restore.
func SetAheadPrefillNoWaitForTest(on bool) func() {
	prev := aheadPrefillNoWait
	aheadPrefillNoWait = on
	return func() { aheadPrefillNoWait = prev }
}

// Ahead runs whose claim did not wait for an unfinished prefill, those whose
// prefill landed on their slot, and those whose prefill did not (dropped:
// skipped, off the covered chain, or the run ended first) (process-wide,
// since start).
var aheadNoWaitClaims, aheadNoWaitLanded, aheadNoWaitFallbacks atomic.Uint64

// AheadPrefillNoWaitStats returns the no-wait claims, the late prefills that
// landed and those that fell back to per-lookup reads (since start).
func AheadPrefillNoWaitStats() (claims, landed, fallbacks uint64) {
	return aheadNoWaitClaims.Load(), aheadNoWaitLanded.Load(), aheadNoWaitFallbacks.Load()
}

// aheadPrefillLandForTest runs on a late prefill's landing goroutine once
// the fill finished, before it lands (tests: hold the landing).
var aheadPrefillLandForTest func()

// aheadLateEndedForTest receives every late prefill once its landing was
// decided (tests: the outcomes).
var aheadLateEndedForTest func(l *aheadLate)

// aheadLate is a base prefill an ahead run's claim did not wait for.
type aheadLate struct {
	ab      *aheadBase
	pr      *aheadPrep // its dropping stops the fill at the next key
	slot    commitment.PrefillSlot
	claimed time.Time
	// The chain the run's layers cover: hash[d] is the block at height
	// top-d; a prefill read at a height in [top-len(hash)+1, top] on that
	// block lands.
	top  uint64
	hash []types.Hash
	// ended closes once the landing goroutine decided.
	ended chan struct{}

	mu     sync.Mutex
	closed bool // the run released: nothing reads through the slot
	// Set before ended closes (under mu).
	landed bool
	skip   string
	at     uint64
	depth  uint64
	// landNs: from the claim to the landing decision; -1 before.
	landNs atomic.Int64
}

// covers reports whether a prefill read on the tree at (at, atHash) lands.
func (l *aheadLate) covers(at uint64, atHash types.Hash) bool {
	if at > l.top {
		return false
	}
	d := l.top - at
	return d < uint64(len(l.hash)) && l.hash[d] == atHash
}

// newAheadLate sets up the late prefill ab of a run on par (its preparation
// pr) and returns it with the run's layers: layers as they are, or, with a
// depth of 2 or more, par's post-state over the kept post-states of its
// ancestors as far down as they are kept continuously (aheadLayersLocked's
// walk), so a prefill read that deep lands.
func (p *lagPipeline) newAheadLate(ab *aheadBase, pr *aheadPrep, par *lagPipeParent, layers []*state.PostState) (*aheadLate, []*state.PostState) {
	l := &aheadLate{ab: ab, pr: pr, claimed: time.Now(), top: par.number, hash: []types.Hash{par.hash}, ended: make(chan struct{})}
	l.landNs.Store(-1)
	if par.number >= 1 {
		l.hash = append(l.hash, par.header.ParentHash)
	}
	if aheadBaseDepth >= 2 && par.number >= 2 {
		if fn := lagPipeAheadLayersForTest; fn != nil {
			fn(p)
		}
		var older []*state.PostState
		p.mu.Lock()
		want, num := par.header.ParentHash, par.number-1
		for num >= 1 && par.number-num < uint64(aheadBaseDepth) {
			var e *lagPipeParent
			for _, o := range p.older {
				if o.hash == want && o.number == num {
					e = o
					break
				}
			}
			if e == nil || e.post == nil || e.header == nil {
				break
			}
			older = append(older, e.post)
			want, num = e.header.ParentHash, num-1
			l.hash = append(l.hash, want)
		}
		p.mu.Unlock()
		if len(older) > 0 {
			layers = append([]*state.PostState{par.post}, older...)
		}
	}
	aheadNoWaitClaims.Add(1)
	go l.land()
	return l, layers
}

// land waits for the fill and publishes it on the slot when the run's
// layers cover it and the run still reads.
func (l *aheadLate) land() {
	ab := l.ab
	<-ab.done
	if fn := aheadPrefillLandForTest; fn != nil {
		fn()
	}
	l.mu.Lock()
	switch {
	case l.closed:
		l.skip = "run ended before the fill"
	case ab.pf == nil:
		l.skip = ab.skip
		if l.skip == "" {
			l.skip = "no prefill"
		}
	case !l.covers(ab.at, ab.atHash):
		if ab.at < l.top+1-uint64(len(l.hash)) {
			l.skip = "an ancestor's post-state is not kept"
		} else {
			l.skip = "tree off the chain at the landing"
		}
	default:
		l.landed, l.at, l.depth = true, ab.at, l.top-ab.at
		l.slot.Set(ab.pf)
	}
	landed := l.landed
	l.landNs.Store(int64(time.Since(l.claimed)))
	l.mu.Unlock()
	if fn := aheadLateEndedForTest; fn != nil {
		fn(l)
	}
	if landed {
		aheadNoWaitLanded.Add(1)
	} else {
		aheadNoWaitFallbacks.Add(1)
		// Nothing reads it: the slot never held it.
		ab.pf.Release()
	}
	close(l.ended)
}

// close ends the run's use of the slot (its readers are done): a fill still
// waiting for the tree or reading stops, and a landed prefill is released.
func (l *aheadLate) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.closed = true
	pf := l.slot.Load()
	l.mu.Unlock()
	if l.pr != nil {
		l.pr.dropping.Store(true)
	}
	l.ab.once.Do(func() { close(l.ab.stop) })
	pf.Release()
}

// stats returns the outcome so far: whether the prefill landed, the time
// from the claim to the landing decision (-1 before it), the depth it was
// read at and why it did not land.
func (l *aheadLate) stats() (landed bool, landMs int64, depth uint64, skip string) {
	if l == nil {
		return false, -1, 0, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	landMs = -1
	if ns := l.landNs.Load(); ns >= 0 {
		landMs = time.Duration(ns).Milliseconds()
	}
	return l.landed, landMs, l.depth, l.skip
}

// claimAheadPrepNoWait is claimAheadPrep for an ahead run with the no-wait
// claim on: it waits for a running preparation as before, but a base
// prefill still waiting for the tree or still reading is left running and
// returned as late (the preparation no longer carries it); one already
// finished is claimed as before.
func (bc *BlockChain) claimAheadPrepNoWait(h types.Hash) (pr *aheadPrep, late *aheadBase, wait time.Duration) {
	if bc == nil {
		return nil, nil, 0
	}
	ps := &bc.aheadPreps
	ps.mu.Lock()
	pr = ps.m[h]
	delete(ps.m, h)
	ps.mu.Unlock()
	if pr == nil || pr.state.CompareAndSwap(prepPending, prepCancelled) || pr.state.Load() == prepCancelled {
		return nil, nil, 0
	}
	t0 := time.Now()
	<-pr.done
	if !pr.ok {
		// No prefill starts for an unusable preparation; halt is a no-op
		// on none.
		pr.base.halt()
		return nil, nil, time.Since(t0)
	}
	if ab := pr.base; ab != nil {
		select {
		case <-ab.done:
			ab.halt()
		default:
			late, pr.base = ab, nil
		}
	}
	return pr, late, time.Since(t0)
}
