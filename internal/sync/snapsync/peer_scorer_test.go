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
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestPeerScorer_UnknownPeersGetNeutralScore(t *testing.T) {
	ps := newPeerScorer()
	ps.mu.Lock()
	score := ps.scoreLocked("peer-a")
	ps.mu.Unlock()
	if score != 0.5 {
		t.Errorf("unknown peer score: want 0.5, got %f", score)
	}
}

func TestPeerScorer_SuccessImprovesScore(t *testing.T) {
	ps := newPeerScorer()
	pid := peer.ID("fast-peer")

	// Record fast successes.
	for i := 0; i < 10; i++ {
		ps.recordSuccess(pid, 50*time.Millisecond)
	}

	ps.mu.Lock()
	score := ps.scoreLocked(pid)
	ps.mu.Unlock()

	// 100% success rate, 50ms avg → score = 1.0 * (1000/50) = 20.0
	if score < 10.0 {
		t.Errorf("fast peer should have high score, got %f", score)
	}
}

func TestPeerScorer_FailureLowersScore(t *testing.T) {
	ps := newPeerScorer()
	pid := peer.ID("flaky-peer")

	ps.recordSuccess(pid, 100*time.Millisecond)
	ps.recordFailure(pid)
	ps.recordFailure(pid)

	ps.mu.Lock()
	score := ps.scoreLocked(pid)
	ps.mu.Unlock()

	// 1/3 success rate + recent failure penalty → very low score
	if score > 1.0 {
		t.Errorf("flaky peer should have low score, got %f", score)
	}
}

func TestPeerScorer_BestPeerPrefersHighScore(t *testing.T) {
	ps := newPeerScorer()
	fast := peer.ID("fast")
	slow := peer.ID("slow")

	for i := 0; i < 20; i++ {
		ps.recordSuccess(fast, 50*time.Millisecond)
		ps.recordSuccess(slow, 500*time.Millisecond)
	}

	// Run 100 picks — the fast peer should be selected most of the time
	// (80% exploitation, 20% exploration).
	fastCount := 0
	for i := 0; i < 100; i++ {
		if ps.bestPeer([]peer.ID{fast, slow}) == fast {
			fastCount++
		}
	}
	if fastCount < 60 {
		t.Errorf("fast peer selected only %d/100 times, expected >60", fastCount)
	}
}

func TestPeerScorer_EmptyCandidates(t *testing.T) {
	ps := newPeerScorer()
	if pid := ps.bestPeer(nil); pid != "" {
		t.Errorf("expected empty peer ID for nil candidates, got %q", pid)
	}
}

func TestPeerScorer_SingleCandidate(t *testing.T) {
	ps := newPeerScorer()
	pid := peer.ID("only")
	if got := ps.bestPeer([]peer.ID{pid}); got != pid {
		t.Errorf("single candidate: want %q, got %q", pid, got)
	}
}

func TestPeerScorer_RecordFailureIgnoresEmptyPeer(t *testing.T) {
	ps := newPeerScorer()
	ps.recordFailure("") // should not panic or create entry
	ps.mu.Lock()
	count := len(ps.peers)
	ps.mu.Unlock()
	if count != 0 {
		t.Errorf("expected no entries after empty peer failure, got %d", count)
	}
}

func TestPeerScorer_BanAfterInvalidResponses(t *testing.T) {
	ps := newPeerScorer()
	pid := peer.ID("malicious")

	// Below threshold — not banned.
	for i := 0; i < banThreshold-1; i++ {
		ps.recordInvalid(pid)
	}
	if ps.isBanned(pid) {
		t.Fatal("peer should not be banned before threshold")
	}

	// Reach threshold — banned.
	ps.recordInvalid(pid)
	if !ps.isBanned(pid) {
		t.Fatal("peer should be banned after reaching threshold")
	}
}

func TestPeerScorer_BannedPeerExcludedFromBestPeer(t *testing.T) {
	ps := newPeerScorer()
	good := peer.ID("good")
	bad := peer.ID("bad")

	// Give good peer a good score.
	for i := 0; i < 10; i++ {
		ps.recordSuccess(good, 50*time.Millisecond)
	}

	// Ban bad peer.
	for i := 0; i < banThreshold; i++ {
		ps.recordInvalid(bad)
	}

	// Bad peer should never be selected.
	for i := 0; i < 50; i++ {
		selected := ps.bestPeer([]peer.ID{good, bad})
		if selected == bad {
			t.Fatal("banned peer should not be selected")
		}
	}
}

func TestPeerScorer_AllPeersBannedReturnsEmpty(t *testing.T) {
	ps := newPeerScorer()
	pid := peer.ID("only-peer")

	for i := 0; i < banThreshold; i++ {
		ps.recordInvalid(pid)
	}

	if got := ps.bestPeer([]peer.ID{pid}); got != "" {
		t.Errorf("expected empty when all peers banned, got %q", got)
	}
}

func TestPeerScorer_RecordInvalidIgnoresEmptyPeer(t *testing.T) {
	ps := newPeerScorer()
	ps.recordInvalid("") // should not panic
	ps.mu.Lock()
	count := len(ps.peers)
	ps.mu.Unlock()
	if count != 0 {
		t.Errorf("expected no entries after empty peer invalid, got %d", count)
	}
}
