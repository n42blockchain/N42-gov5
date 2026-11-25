// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestTimeoutPathScale100 is the AM22(b)/AM17 in-process scale check of the
// timeout-certificate path: 100 validators at tenure 1, f = 33 of them out
// (32 silent from the start, one leader killed mid-run). Every view led by
// an absent member times out, a TC forms from the 67 live members, and the
// chain keeps committing. The test reports the BLS pairing checks per live
// node per timed-out view (the measurement of AM22(b)).
func TestTimeoutPathScale100(t *testing.T) {
	if testing.Short() {
		t.Skip("100-engine scale test")
	}
	const n = 100
	s := newRotSim(t, n, 1, true)
	for _, e := range s.engines {
		e.EnableH2V4(H2V4ChainIdentity{ChainID: 4242, GenesisHash: types.Hash{0x42}})
	}
	// 32 silent members: every third index from 4 (views 4, 7, ... 97 lead
	// to a dead member). Index 1 stays alive until it is killed below.
	silent := 0
	for i := 4; i < n && silent < 32; i += 3 {
		s.alive[i] = false
		silent++
	}
	if silent != 32 {
		t.Fatalf("silent members %d, want 32", silent)
	}

	// Healthy start: views 1..3 commit (leader of view 1 is index 1).
	if !s.runUntil(time.Minute, func() bool { return s.maxCommitView() >= 3 }) {
		t.Fatalf("healthy views did not commit (max %d)", s.maxCommitView())
	}
	// Kill a leader: index 101 % 100 = 1 leads view 101; kill it now (it is
	// a member that already led and is now the 33rd absent one).
	s.alive[1] = false
	alive := 0
	for _, a := range s.alive {
		if a {
			alive++
		}
	}
	if alive != n-int(s.setup.f) {
		t.Fatalf("alive %d, want %d", alive, n-int(s.setup.f))
	}

	var timeoutOps, timeoutViews uint64
	const target = ViewNumber(14) // views 4, 7, 10, 13 time out
	for s.now <= 30*time.Minute && s.maxCommitView() < target {
		before := PairingChecks()
		proposed := len(s.proposed)
		views := s.engines[0].CurrentView()
		s.step()
		if len(s.proposed) == proposed && s.engines[0].CurrentView() > views {
			timeoutOps += PairingChecks() - before
			timeoutViews++
		}
	}
	if s.maxCommitView() < target {
		t.Fatalf("chain did not progress past the dead leaders (max commit view %d)", s.maxCommitView())
	}
	if timeoutViews < 4 {
		t.Fatalf("timed-out views %d, want >= 4", timeoutViews)
	}
	// Every live node holds a TC that verifies.
	for i, e := range s.engines {
		if !s.alive[i] {
			continue
		}
		tc := e.roundState.HighestTC()
		if tc == nil {
			t.Fatalf("node %d holds no TC", i)
		}
		if err := e.verifyTC(tc); err != nil {
			t.Fatalf("node %d TC view %d does not verify: %v", i, tc.View, err)
		}
	}
	perNodeView := float64(timeoutOps) / float64(uint64(alive)*timeoutViews)
	t.Logf("n=%d live=%d timed-out views=%d pairing checks=%d per live node per timed-out view=%.1f",
		n, alive, timeoutViews, timeoutOps, perNodeView)
	// Before AM22(b) this was 135 (one pairing per timeout signature plus
	// one per carried high QC); batched verification and the certificate
	// cache bring it to one batch check per node per timed-out view.
	if perNodeView > 3 {
		t.Fatalf("timeout path is %.1f pairing checks per node per timed-out view, want <= 3", perNodeView)
	}
}
