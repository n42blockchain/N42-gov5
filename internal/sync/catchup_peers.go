// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Catch-up peer selection, timeouts and scoring (sync review S0d / C6).
//
// The range catch-up used to ask Connected()[:5] -- an unsorted slice that
// could hold behind peers and light nodes -- in series, with a fixed 30 s
// timeout each, and never scored a peer for a failed, empty or invalid
// response. This file keeps a small per-peer book and decides, per round:
//
//   - which peers to ask: every connected peer that is not classified bad,
//     ordered by (1) whether its advertised head is backed -- at or below the
//     verified CommitQC watermark or the (f+1)-th highest advertised height --
//     (2) recent failures, (3) advertised height. Peers in back-off are used
//     only when nothing else is left. At most catchUpMaxPeersPerRound are
//     asked per round;
//   - when a prefix is not enough: a peer that served only part of the
//     range is followed by the next peer that claims more, with the same
//     range (equal-height fork recovery needs the start block from it);
//   - how long to wait: 3 s plus four times the peer's observed per-block
//     latency for the requested count, bounded to [5 s, 30 s]; 30 s until a
//     peer has served once;
//   - what a failure costs: a timeout, an empty or failed response for blocks
//     within the peer's own claim, a malformed response, or a block that
//     fails execution puts the peer in exponential back-off and, except for
//     a bare timeout for blocks above its claim, adds a bad response to the
//     existing peer scorer. A peer whose claim is not backed by blocks it serves is
//     thereby penalised and its claim stops steering the catch-up target.
//
// N42_SYNC_LEGACY_PEER_SELECT=1 restores Connected()[:5], 30 s and no scoring.

package sync

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	gosync "sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/synclegacy"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

const (
	catchUpMaxPeersPerRound = 8
	catchUpTimeoutMax       = 30 * time.Second
	catchUpTimeoutMin       = 5 * time.Second
	catchUpTimeoutBase      = 3 * time.Second
	catchUpBackoffBase      = 2 * time.Second
	catchUpBackoffMax       = 5 * time.Minute
	maxCatchUpPeerBook      = 1024
	// catchUpLatencyWeight is the EWMA weight of the newest per-block latency
	// sample, in percent.
	catchUpLatencyWeight = 30
)

// catchUpPeerState is what catch-up remembers about one peer.
type catchUpPeerState struct {
	failures int
	until    time.Time     // back-off ends
	perBlock time.Duration // EWMA of response time per served block
	samples  int
}

// catchUpPeerBook is the per-peer record. Bounded; a full book is reset.
type catchUpPeerBook struct {
	mu    gosync.Mutex
	peers map[peer.ID]*catchUpPeerState
}

func (b *catchUpPeerBook) get(pid peer.ID) *catchUpPeerState {
	if b.peers == nil {
		b.peers = make(map[peer.ID]*catchUpPeerState)
	}
	st, ok := b.peers[pid]
	if !ok {
		if len(b.peers) >= maxCatchUpPeerBook {
			b.peers = make(map[peer.ID]*catchUpPeerState)
		}
		st = &catchUpPeerState{}
		b.peers[pid] = st
	}
	return st
}

func (b *catchUpPeerBook) snapshot(pid peer.ID) catchUpPeerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st, ok := b.peers[pid]; ok {
		return *st
	}
	return catchUpPeerState{}
}

func (b *catchUpPeerBook) fail(pid peer.ID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(pid)
	st.failures++
	shift := st.failures - 1
	if shift > 16 {
		shift = 16
	}
	d := catchUpBackoffBase << uint(shift)
	if d > catchUpBackoffMax || d <= 0 {
		d = catchUpBackoffMax
	}
	st.until = now.Add(d)
}

func (b *catchUpPeerBook) served(pid peer.ID, blocks int, took time.Duration) {
	if blocks <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(pid)
	st.failures = 0
	st.until = time.Time{}
	sample := took / time.Duration(blocks)
	if st.samples == 0 {
		st.perBlock = sample
	} else {
		st.perBlock = (st.perBlock*(100-catchUpLatencyWeight) + sample*catchUpLatencyWeight) / 100
	}
	st.samples++
}

// catchUpTimeout derives the request timeout for count blocks from pid.
func catchUpTimeout(st catchUpPeerState, count uint64) time.Duration {
	if st.samples == 0 {
		return catchUpTimeoutMax
	}
	if count == 0 {
		count = 1
	}
	if st.perBlock > 0 && uint64(catchUpTimeoutMax/st.perBlock) < 4*count {
		return catchUpTimeoutMax
	}
	t := catchUpTimeoutBase + 4*st.perBlock*time.Duration(count)
	if t < catchUpTimeoutMin {
		t = catchUpTimeoutMin
	}
	if t > catchUpTimeoutMax {
		t = catchUpTimeoutMax
	}
	return t
}

// catchUpClaimQuorum is the f+1 the consensus layer last passed to
// HeightBehindQuorum, or a third of the connected peers plus one before the
// first call.
func (s *Service) catchUpClaimQuorum(connected int) int {
	if q := int(s.claimQuorum.Load()); q > 0 {
		return q
	}
	return connected/3 + 1
}

// catchUpBackedHeight is the highest height catch-up may treat as backed: the
// verified CommitQC watermark or the quorum height, whichever is higher.
func (s *Service) catchUpBackedHeight(self *uint256.Int, connected int) uint64 {
	backed := s.verifiedCommitTarget.Load()
	if q, _ := s.cfg.p2p.Peers().QuorumHeight(s.catchUpClaimQuorum(connected), self); q != nil && q.Uint64() > backed {
		backed = q.Uint64()
	}
	return backed
}

// catchUpPeerSet returns the peers a catch-up round may draw from: the first
// five connected peers on the legacy path, every connected peer that is not
// classified bad otherwise (catchUpTo orders and caps them).
func (s *Service) catchUpPeerSet() []peer.ID {
	connected := s.cfg.p2p.Peers().Connected()
	if synclegacy.PeerSelect.On() {
		if len(connected) > 5 {
			connected = connected[:5]
		}
		return connected
	}
	out := make([]peer.ID, 0, len(connected))
	for _, pid := range connected {
		if s.cfg.p2p.Peers().IsBad(pid) {
			continue
		}
		out = append(out, pid)
	}
	return out
}

type catchUpCandidate struct {
	pid       peer.ID
	claim     uint64
	known     bool
	tier      int
	failures  int
	backedOff bool
}

// orderCatchUpPeers orders peers for a round starting at start and caps the
// list at catchUpMaxPeersPerRound. Legacy: unchanged.
func (s *Service) orderCatchUpPeers(peers []peer.ID, start uint64) []peer.ID {
	if synclegacy.PeerSelect.On() || len(peers) == 0 {
		return peers
	}
	self := currentBlockNumber(s.cfg.chain)
	backed := s.catchUpBackedHeight(self, len(peers))
	now := time.Now()
	cands := make([]catchUpCandidate, 0, len(peers))
	for _, pid := range peers {
		c := catchUpCandidate{pid: pid, tier: 2}
		if h := s.cfg.p2p.Peers().PeerHeight(pid); h != nil {
			c.claim, c.known = h.Uint64(), true
			if c.claim >= start {
				c.tier = 1
				if c.claim <= backed {
					c.tier = 0
				}
			}
		}
		st := s.catchUpBook.snapshot(pid)
		c.failures = st.failures
		c.backedOff = now.Before(st.until)
		cands = append(cands, c)
	}
	rand.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.backedOff != b.backedOff {
			return !a.backedOff
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.failures != b.failures {
			return a.failures < b.failures
		}
		return a.claim > b.claim
	})
	out := make([]peer.ID, 0, len(cands))
	for _, c := range cands {
		if c.backedOff && len(out) > 0 {
			break // back off: only when nothing else is left
		}
		out = append(out, c.pid)
		if len(out) == catchUpMaxPeersPerRound {
			break
		}
	}
	return out
}

// catchUpPeerRequest returns the timeout for asking pid for req, and whether
// every requested block lies at or below the height pid advertised. Only then
// is failing to serve them evidence against the peer's claim. The request is
// not clamped to the claim: Status heights refresh every few minutes while
// CommitQC targets run ahead of them, and a peer serves the stored prefix of
// a longer range.
func (s *Service) catchUpPeerRequest(pid peer.ID, req *sync_pb.BodiesByRangeRequest, end uint64) (time.Duration, bool) {
	if synclegacy.PeerSelect.On() {
		return catchUpTimeoutMax, false
	}
	h := s.cfg.p2p.Peers().PeerHeight(pid)
	withinClaim := h != nil && h.Uint64() >= end
	return catchUpTimeout(s.catchUpBook.snapshot(pid), req.Count), withinClaim
}

// catchUpPenalise adds a bad response to pid in the peer scorer.
func (s *Service) catchUpPenalise(pid peer.ID) {
	s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(pid)
}

// noteCatchUpRequestFailed records a failed range request.
func (s *Service) noteCatchUpRequestFailed(pid peer.ID, err error, withinClaim bool) {
	if synclegacy.PeerSelect.On() {
		return
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	malformed := errors.Is(err, ErrInvalidFetchedData)
	if !timedOut && !malformed && !withinClaim {
		return // asked beyond what the peer claims: no evidence against it
	}
	s.catchUpBook.fail(pid, time.Now())
	if malformed || withinClaim {
		s.catchUpPenalise(pid)
	}
}

// noteCatchUpEmpty records a response without a single block.
func (s *Service) noteCatchUpEmpty(pid peer.ID, withinClaim bool) {
	if synclegacy.PeerSelect.On() || !withinClaim {
		return
	}
	s.catchUpBook.fail(pid, time.Now())
	s.catchUpPenalise(pid)
}

// noteCatchUpInvalid records a response carrying a block that is known bad
// or fails execution.
func (s *Service) noteCatchUpInvalid(pid peer.ID) {
	if synclegacy.PeerSelect.On() {
		return
	}
	s.catchUpBook.fail(pid, time.Now())
	s.catchUpPenalise(pid)
}

// noteCatchUpServed records a response of blocks blocks that took took.
func (s *Service) noteCatchUpServed(pid peer.ID, blocks int, took time.Duration) {
	if synclegacy.PeerSelect.On() {
		return
	}
	s.catchUpBook.served(pid, blocks, took)
}

// catchUpCanExtend reports whether one of the remaining peers advertises a
// height above reached, so a round that imported only a prefix (one peer's
// stored chain, or a lagging fork group) should keep going. Peers without a
// claim, or with a claim at or below reached, cannot be shown to add
// anything and end the round as before.
func (s *Service) catchUpCanExtend(rest []peer.ID, reached, highest uint64) bool {
	if synclegacy.PeerSelect.On() || reached >= highest {
		return false
	}
	for _, pid := range rest {
		if h := s.cfg.p2p.Peers().PeerHeight(pid); h != nil && h.Uint64() > reached {
			return true
		}
	}
	return false
}

// catchUpHighestClaim is CatchUp's target: the highest height advertised by
// a peer that is neither classified bad nor in catch-up back-off. A peer
// whose claim failed to produce blocks therefore stops steering the target.
func (s *Service) catchUpHighestClaim(self *uint256.Int) (uint64, bool) {
	now := time.Now()
	var best uint64
	found := false
	for _, pid := range s.cfg.p2p.Peers().Connected() {
		if s.cfg.p2p.Peers().IsBad(pid) || now.Before(s.catchUpBook.snapshot(pid).until) {
			continue
		}
		h := s.cfg.p2p.Peers().PeerHeight(pid)
		if h == nil || h.Cmp(self) <= 0 {
			continue
		}
		if v := h.Uint64(); !found || v > best {
			best, found = v, true
		}
	}
	return best, found
}
