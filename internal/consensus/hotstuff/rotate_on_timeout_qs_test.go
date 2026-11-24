package hotstuff

import (
	"testing"
	"time"
)

// TestRotateOnTimeoutProcessTenure: the qs fleet sets its tenure through
// N42_HOTSTUFF_LEADER_TENURE (the process-wide leaderTenure), not the
// chainspec, so the engine's own leaderTenure stays 0. Rotation must jump by
// that same tenure: with validator 1 (views 8..15) silent, validator 2 leads
// from view 16 within ~2x the base timeout.
func TestRotateOnTimeoutProcessTenure(t *testing.T) {
	saved := leaderTenure
	leaderTenure = 8
	t.Cleanup(func() { leaderTenure = saved })

	s := newRotSim(t, 7, 8, true)
	for _, e := range s.engines {
		e.leaderTenure = 0 // adapter-less engine: falls back to the process tenure
	}
	if got := s.engines[0].viewAfterTC(9); got != 16 {
		t.Fatalf("viewAfterTC(9) with process tenure 8 = %d, want 16", got)
	}
	s.alive[1] = false
	if !s.runUntil(5*time.Minute, func() bool { return s.maxCommitView() >= 24 }) {
		t.Fatalf("chain did not reach view 24 (max %d)", s.maxCommitView())
	}
	last := s.lastCommitBefore(8)
	first := s.commitsAfter(7)[0]
	if first.view != 16 || first.leader != 2 {
		t.Fatalf("first post-stall commit view=%d leader=%d, want view 16 leader 2", first.view, first.leader)
	}
	if gap := first.at - last.at; gap > 2*rotSimBase {
		t.Fatalf("stall %v exceeds 2x base timeout", gap)
	}
}
