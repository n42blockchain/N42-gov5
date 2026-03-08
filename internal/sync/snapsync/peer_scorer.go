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

// peerStats tracks per-peer download performance.
type peerStats struct {
	successes    int
	failures     int
	totalLatency time.Duration
	lastFailure  time.Time
}

// peerScorer selects the best peer for each download task based on
// observed success rates and response latencies.
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

// bestPeer returns the highest-scoring connected peer, with occasional random
// exploration to avoid starvation.
func (ps *peerScorer) bestPeer(candidates []peer.ID) peer.ID {
	if len(candidates) == 0 {
		return ""
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Explore: pick a random peer to give every peer a chance.
	if len(candidates) > 1 && rand.Intn(explorationRate) == 0 {
		return candidates[rand.Intn(len(candidates))]
	}

	best := candidates[0]
	bestScore := ps.scoreLocked(best)
	for _, pid := range candidates[1:] {
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
