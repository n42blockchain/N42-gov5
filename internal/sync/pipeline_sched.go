// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Slot scheduler of the parallel catch-up pipeline (sync v2 design §4.1,
// standard §6.2 / §6.4). Pure bookkeeping, no I/O: the pipeline run feeds it
// the importer cursor, the target, the reorder buffer and the candidate
// peers, and it returns the requests to issue.
//
//   - Slots: the lowest number that is neither buffered nor requested starts
//     the next slot. Its count is sized from the measured block size
//     (slotBytes / EWMA block bytes, at most slotBlocks) and ends before the
//     next buffered or requested number. A failed, short or refused request
//     simply leaves its numbers unbuffered, so they are re-planned (to the
//     best peer at that moment) on the next pass. Received prefixes stay.
//   - Window and back-pressure: requests never reach beyond cursor +
//     bufferBlocks, and buffered plus reserved blocks and bytes stay within
//     the buffer bounds. Only an empty pipeline may exceed the byte bound,
//     with one block, so the importer always makes progress.
//   - Peers: stage-A tiers (claim backed by the verified commit watermark or
//     the quorum height, then any claim covering the slot, then the rest),
//     peers in catch-up back-off only when no other peer is left, a peer
//     whose last reply ended short below the slot recently goes last, then
//     the highest measured throughput. A peer may have at most its adaptive
//     limit in flight: 1 at first, raised to peerInflight while its
//     throughput keeps up with the best peer, back to 1 on any failure.
//   - Timeouts: from the peer's RTT and throughput EWMAs, within stage-A
//     bounds (whole request 5-30 s, first chunk 2-20 s; 10 s / 30 s while a
//     peer has no sample).
//   - Head of line: when the request covering the cursor is overdue by more
//     than twice its expected time and another peer is idle, one duplicate
//     is issued; the first copy to arrive wins.

package sync

import (
	"math"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	pipeEWMAWeight      = 0.3
	pipeFirstChunkMin   = 2 * time.Second
	pipeFirstChunkMax   = 20 * time.Second
	pipeFirstChunkUnset = 10 * time.Second
	pipeShortMemory     = 10 * time.Second
	pipeBackoffBase     = 500 * time.Millisecond
	pipeBackoffMax      = 30 * time.Second
)

// pipePeerStats is the per-peer estimate of one pipeline run.
type pipePeerStats struct {
	limit    int
	inflight int
	rtt      time.Duration // EWMA time to the first block
	tput     float64       // EWMA bytes per second
	samples  int
	// shortTop is the last block of the latest reply that ended below what
	// was asked (at shortAt): the peer's observed availability.
	shortTop uint64
	shortAt  time.Time
	// failures / until: run-local back-off after a failed, refused, empty
	// or unusable reply. Never a score: stage A decides what is scored.
	failures int
	until    time.Time
}

// pipeReq is one bodies_by_range request in flight.
type pipeReq struct {
	id         uint64
	pid        peer.ID
	start, end uint64
	estBytes   uint64
	issued     time.Time
	expected   time.Duration // 0 while the peer has no sample
	firstTO    time.Duration
	wholeTO    time.Duration
	dup        bool
}

func (r *pipeReq) count() uint64 { return r.end - r.start + 1 }

// pipeCandidate is what the scheduler needs to know about a connected peer.
type pipeCandidate struct {
	pid       peer.ID
	claim     uint64
	known     bool
	backedOff bool
	failures  int
}

type pipeScheduler struct {
	cfg        pipelineConfig
	peers      map[peer.ID]*pipePeerStats
	inflight   map[uint64]*pipeReq
	nextID     uint64
	blockBytes float64 // EWMA of served block bytes; 0 = unknown
}

func newPipeScheduler(cfg pipelineConfig) *pipeScheduler {
	if cfg.peerInflight < 1 {
		cfg.peerInflight = 1
	}
	if cfg.slotBlocks < 1 {
		cfg.slotBlocks = 1
	}
	return &pipeScheduler{cfg: cfg, peers: make(map[peer.ID]*pipePeerStats), inflight: make(map[uint64]*pipeReq)}
}

func (sc *pipeScheduler) stats(pid peer.ID) *pipePeerStats {
	st, ok := sc.peers[pid]
	if !ok {
		st = &pipePeerStats{limit: 1}
		sc.peers[pid] = st
	}
	return st
}

// covering returns the first non-duplicate request in flight covering n.
func (sc *pipeScheduler) covering(n uint64) *pipeReq {
	for _, r := range sc.inflight {
		if !r.dup && r.start <= n && n <= r.end {
			return r
		}
	}
	return nil
}

func (sc *pipeScheduler) hasDup(n uint64) bool {
	for _, r := range sc.inflight {
		if r.dup && r.start <= n && n <= r.end {
			return true
		}
	}
	return false
}

// reserved is the blocks and estimated bytes requested but not received.
func (sc *pipeScheduler) reserved() (blocks int, bytes uint64) {
	for _, r := range sc.inflight {
		if r.dup {
			continue
		}
		blocks += int(r.count())
		bytes += r.estBytes
	}
	return blocks, bytes
}

func (sc *pipeScheduler) estBlockBytes() uint64 {
	if sc.blockBytes <= 0 {
		return 0
	}
	return uint64(sc.blockBytes)
}

// slotCount is the blocks of a new slot.
func (sc *pipeScheduler) slotCount() uint64 {
	if bb := sc.estBlockBytes(); bb > 0 {
		n := sc.cfg.slotBytes / bb
		if n < 1 {
			n = 1
		}
		if n > sc.cfg.slotBlocks {
			n = sc.cfg.slotBlocks
		}
		return n
	}
	return sc.cfg.slotBlocks
}

// timeouts derives the first-chunk and whole-request timeouts and the
// expected duration of a request of estBytes from pid.
func (sc *pipeScheduler) timeouts(st *pipePeerStats, estBytes uint64) (first, whole, expected time.Duration) {
	if st.samples == 0 || st.tput <= 0 {
		return pipeFirstChunkUnset, catchUpTimeoutMax, 0
	}
	transfer := time.Duration(float64(estBytes) / st.tput * float64(time.Second))
	expected = st.rtt + transfer
	first = clampDur(3*st.rtt, pipeFirstChunkMin, pipeFirstChunkMax)
	whole = clampDur(3*st.rtt+2*transfer, catchUpTimeoutMin, catchUpTimeoutMax)
	return first, whole, expected
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// rank orders candidates for a slot starting at start. backed is the
// verified commit watermark or quorum height (stage A).
func (sc *pipeScheduler) rank(cands []pipeCandidate, start, backed uint64, now time.Time) []pipeCandidate {
	anyActive := false
	for _, c := range cands {
		if !c.backedOff {
			anyActive = true
			break
		}
	}
	type ranked struct {
		c    pipeCandidate
		tier int
		tput float64
	}
	best := 0.0
	for _, st := range sc.peers {
		if st.samples > 0 && st.tput > best {
			best = st.tput
		}
	}
	rs := make([]ranked, 0, len(cands))
	for _, c := range cands {
		if anyActive && c.backedOff {
			continue
		}
		st := sc.stats(c.pid)
		if now.Before(st.until) {
			continue // run-local back-off
		}
		r := ranked{c: c, tier: 2}
		if c.known && c.claim >= start {
			r.tier = 1
			if c.claim <= backed {
				r.tier = 0
			}
		}
		if !st.shortAt.IsZero() && now.Sub(st.shortAt) < pipeShortMemory && st.shortTop < start {
			r.tier += 3
		}
		r.tput = st.tput
		if st.samples == 0 {
			r.tput = math.Max(best, 1) // optimistic: try every peer once
		}
		rs = append(rs, r)
	}
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.tput != b.tput {
			return a.tput > b.tput
		}
		return a.c.failures < b.c.failures
	})
	out := make([]pipeCandidate, len(rs))
	for i := range rs {
		out[i] = rs[i].c
	}
	return out
}

func (sc *pipeScheduler) issue(pid peer.ID, start, end uint64, now time.Time, dup bool) *pipeReq {
	st := sc.stats(pid)
	est := sc.estBlockBytes() * (end - start + 1)
	first, whole, expected := sc.timeouts(st, est)
	sc.nextID++
	r := &pipeReq{id: sc.nextID, pid: pid, start: start, end: end, estBytes: est, issued: now,
		expected: expected, firstTO: first, wholeTO: whole, dup: dup}
	sc.inflight[r.id] = r
	st.inflight++
	return r
}

// plan returns the requests to issue now. cursor is the next number the
// importer needs, target the highest number to fetch.
func (sc *pipeScheduler) plan(now time.Time, cursor, target uint64, buf *reorderBuffer, cands []pipeCandidate, backed uint64) []*pipeReq {
	if cursor > target || len(cands) == 0 {
		return nil
	}
	windowEnd := cursor + uint64(sc.cfg.bufferBlocks) - 1
	if windowEnd > target {
		windowEnd = target
	}
	var out []*pipeReq
	n := cursor
	for n <= windowEnd {
		if buf.has(n) {
			n++
			continue
		}
		if r := sc.covering(n); r != nil {
			n = r.end + 1
			continue
		}
		count := sc.slotCount()
		end := n + count - 1
		if end > windowEnd {
			end = windowEnd
		}
		for m := n + 1; m <= end; m++ {
			if buf.has(m) || sc.covering(m) != nil {
				end = m - 1
				break
			}
		}
		// Back-pressure: buffered + reserved stays within the bounds.
		resBlocks, resBytes := sc.reserved()
		used := buf.len() + resBlocks
		if room := sc.cfg.bufferBlocks - used; room < int(end-n+1) {
			if room <= 0 {
				break
			}
			end = n + uint64(room) - 1
		}
		if bb := sc.estBlockBytes(); bb > 0 {
			usedBytes := buf.size() + resBytes
			var roomBlocks uint64
			if usedBytes < sc.cfg.bufferBytes {
				roomBlocks = (sc.cfg.bufferBytes - usedBytes) / bb
			}
			if roomBlocks == 0 {
				if buf.len() > 0 || len(sc.inflight) > 0 {
					break
				}
				roomBlocks = 1 // an empty pipeline always fetches the next block
			}
			if roomBlocks < end-n+1 {
				end = n + roomBlocks - 1
			}
		}
		var chosen *pipeCandidate
		for _, c := range sc.rank(cands, n, backed, now) {
			if st := sc.stats(c.pid); st.inflight < st.limit {
				c := c
				chosen = &c
				break
			}
		}
		if chosen == nil {
			break // every peer is at its limit
		}
		out = append(out, sc.issue(chosen.pid, n, end, now, false))
		n = end + 1
	}

	// Head of line: duplicate an overdue request for the cursor once.
	if !buf.has(cursor) && !sc.hasDup(cursor) {
		if r := sc.covering(cursor); r != nil && r.expected > 0 && now.Sub(r.issued) > 2*r.expected {
			for _, c := range sc.rank(cands, cursor, backed, now) {
				if c.pid == r.pid {
					continue
				}
				if st := sc.stats(c.pid); st.inflight < st.limit {
					out = append(out, sc.issue(c.pid, cursor, r.end, now, true))
					break
				}
			}
		}
	}
	return out
}

// complete records the outcome of r. served blocks totalling bytes arrived,
// the first after firstLatency and the last after took; ok is false for a
// failed, refused, empty or timed-out request.
func (sc *pipeScheduler) complete(r *pipeReq, ok bool, served int, bytes uint64, firstLatency, took time.Duration, now time.Time) {
	if _, live := sc.inflight[r.id]; !live {
		return
	}
	delete(sc.inflight, r.id)
	st := sc.stats(r.pid)
	if st.inflight > 0 {
		st.inflight--
	}
	if !ok || served == 0 {
		sc.backoff(r.pid, now)
		return
	}
	st.failures, st.until = 0, time.Time{}
	if took <= 0 {
		took = time.Millisecond
	}
	tput := float64(bytes) / took.Seconds()
	if st.samples == 0 {
		st.rtt, st.tput = firstLatency, tput
	} else {
		st.rtt = time.Duration((1-pipeEWMAWeight)*float64(st.rtt) + pipeEWMAWeight*float64(firstLatency))
		st.tput = (1-pipeEWMAWeight)*st.tput + pipeEWMAWeight*tput
	}
	st.samples++
	if per := float64(bytes) / float64(served); sc.blockBytes == 0 {
		sc.blockBytes = per
	} else {
		sc.blockBytes = (1-pipeEWMAWeight)*sc.blockBytes + pipeEWMAWeight*per
	}
	best := 0.0
	for _, o := range sc.peers {
		if o.samples > 0 && o.tput > best {
			best = o.tput
		}
	}
	if st.tput*2 >= best {
		if st.limit < sc.cfg.peerInflight {
			st.limit++
		}
	} else {
		st.limit = 1
	}
	if last := r.start + uint64(served) - 1; last < r.end {
		st.shortTop, st.shortAt = last, now
	}
}

// backoff puts pid in run-local back-off and drops its in-flight limit to 1.
func (sc *pipeScheduler) backoff(pid peer.ID, now time.Time) {
	st := sc.stats(pid)
	st.limit = 1
	st.failures++
	shift := st.failures - 1
	if shift > 16 {
		shift = 16
	}
	d := pipeBackoffBase << uint(shift)
	if d > pipeBackoffMax || d <= 0 {
		d = pipeBackoffMax
	}
	st.until = now.Add(d)
}

// cancelAll forgets every request in flight (the run is ending).
func (sc *pipeScheduler) cancelAll() {
	for id, r := range sc.inflight {
		if st := sc.peers[r.pid]; st != nil && st.inflight > 0 {
			st.inflight--
		}
		delete(sc.inflight, id)
	}
}
