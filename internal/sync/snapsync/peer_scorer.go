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
//
// peerScorer ranks snap-sync peers by observed success rate and
// response latency. Uses an explorationRate to sample non-best peers,
// applies a failurePenaltyWindow on errors and bans peers that exceed
// banThreshold invalid responses for banDuration.

package snapsync

import (
	"math/rand"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// explorationRate is the probability of picking a random peer instead of the
// best-scored one.  This prevents starvation of new or slow-but-recovering peers.
const explorationRate = 5 // 1 in 5 = 20%

// failurePenaltyWindow is the duration after a failure during which a peer's
// score is significantly reduced to allow other peers to be tried first.
const failurePenaltyWindow = 10 * time.Second

// banThreshold is the number of invalid responses that triggers a peer ban.
// Invalid responses indicate data corruption or malicious behavior.
const banThreshold = 3

// banDuration is how long a banned peer remains excluded.
const banDuration = 5 * time.Minute

// peerStats tracks per-peer download performance.
type peerStats struct {
	successes    int
	failures     int
	invalids     int // count of invalid/malicious responses
	totalLatency time.Duration
	lastFailure  time.Time
	bannedUntil  time.Time
}

// peerScorer selects the best peer for each download task based on
// observed success rates and response latencies. Peers that send
// invalid data are temporarily banned.
type peerScorer struct {
	mu    sync.Mutex
	peers map[peer.ID]*peerStats
}

func newPeerScorer() *peerScorer {
	return &peerScorer{
		peers: make(map[peer.ID]*peerStats),
	}
}

// recordSuccess records a successful response from the given peer.
func (ps *peerScorer) recordSuccess(pid peer.ID, latency time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := ps.getOrCreate(pid)
	s.successes++
	s.totalLatency += latency
}

// recordFailure records a failed request to the given peer.
func (ps *peerScorer) recordFailure(pid peer.ID) {
	if pid == "" {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := ps.getOrCreate(pid)
	s.failures++
	s.lastFailure = time.Now()
}

// recordInvalid records an invalid/malicious response from the given peer.
// After banThreshold invalid responses, the peer is banned for banDuration.
func (ps *peerScorer) recordInvalid(pid peer.ID) {
	if pid == "" {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := ps.getOrCreate(pid)
	s.invalids++
	s.failures++
	s.lastFailure = time.Now()
	if s.invalids >= banThreshold {
		s.bannedUntil = time.Now().Add(banDuration)
	}
}

// isBanned returns true if the peer is currently banned.
func (ps *peerScorer) isBanned(pid peer.ID) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s, ok := ps.peers[pid]
	if !ok {
		return false
	}
	return !s.bannedUntil.IsZero() && time.Now().Before(s.bannedUntil)
}

// bestPeer returns the highest-scoring connected peer, with occasional random
// exploration to avoid starvation. Banned peers are excluded.
func (ps *peerScorer) bestPeer(candidates []peer.ID) peer.ID {
	if len(candidates) == 0 {
		return ""
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Filter out banned peers.
	now := time.Now()
	var eligible []peer.ID
	for _, pid := range candidates {
		s, ok := ps.peers[pid]
		if !ok || s.bannedUntil.IsZero() || now.After(s.bannedUntil) {
			eligible = append(eligible, pid)
		}
	}
	if len(eligible) == 0 {
		return ""
	}

	// Explore: pick a random peer to give every peer a chance.
	if len(eligible) > 1 && rand.Intn(explorationRate) == 0 {
		return eligible[rand.Intn(len(eligible))]
	}

	best := eligible[0]
	bestScore := ps.scoreLocked(best)
	for _, pid := range eligible[1:] {
		s := ps.scoreLocked(pid)
		if s > bestScore {
			best = pid
			bestScore = s
		}
	}
	return best
}

// scoreLocked computes a score for the given peer.  Higher is better.
// Caller must hold ps.mu.
func (ps *peerScorer) scoreLocked(pid peer.ID) float64 {
	s, ok := ps.peers[pid]
	if !ok {
		return 0.5 // neutral score for unknown peers
	}
	total := s.successes + s.failures
	if total == 0 {
		return 0.5
	}

	successRate := float64(s.successes) / float64(total)

	// Average latency factor: faster peers score higher.
	avgMs := float64(1000) // default 1 second for peers with no successes
	if s.successes > 0 {
		avgMs = float64(s.totalLatency.Milliseconds()) / float64(s.successes)
		if avgMs < 1 {
			avgMs = 1
		}
	}

	score := successRate * (1000.0 / avgMs)

	// Heavily penalize peers that failed recently.
	if !s.lastFailure.IsZero() && time.Since(s.lastFailure) < failurePenaltyWindow {
		score *= 0.1
	}

	// Extra penalty for invalid responses.
	if s.invalids > 0 {
		score *= 0.5
	}

	return score
}

func (ps *peerScorer) getOrCreate(pid peer.ID) *peerStats {
	s, ok := ps.peers[pid]
	if !ok {
		s = &peerStats{}
		ps.peers[pid] = s
	}
	return s
}
