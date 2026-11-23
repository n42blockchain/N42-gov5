package sync

import (
	"testing"
)

// Multi-peer catch-up scenario on the in-process harness: a validator of a
// 7-node network (f=2) was stopped at 100 while the six others advanced to
// 230, and one extra connected peer lies about its height. The restarted node
// must not let the liar gate production, must catch up to what the honest
// peers store, penalise the liar, and then follow a CommitQC target that runs
// ahead of the peers' stale Status heights.
//
// Not covered here: rejoining HotStuff voting. That needs real consensus
// engines with real chains; no in-process harness wires both (the hotstuff
// chaos harness has engines only, the sync tests chains only).
func TestCatchUpScenarioRestartBehindWithLyingPeer(t *testing.T) {
	all := harnessBase(240)
	chain := newHarnessChain(all[:101]...) // stopped at 100
	net := newHarnessNet(t, chain)
	validators := make([]*harnessPeer, 0, 6)
	for i := 0; i < 6; i++ {
		// Stored up to 235, advertised 230 (Status refreshes lag commits).
		validators = append(validators, net.addPeer(230, harnessHonest, all[:236]...))
	}
	liar := net.addPeer(1_000_000_000, harnessEmpty)

	// Gate before catching up: the honest gap counts, the liar does not.
	if got := net.svc.HeightBehindQuorum(3); got != 130 {
		t.Fatalf("gate gap = %d, want 130 (the f+1-th honest height), not the liar's", got)
	}

	net.drain(10)
	// Everything the peers store is fetched, even above their stale claims.
	if got := chain.headNumber(); got != 235 {
		t.Fatalf("head after catch-up = %d, want 235", got)
	}
	if got := net.svc.HeightBehindQuorum(3); got != 0 {
		t.Fatalf("gate gap after catch-up = %d, want 0: the liar must not stop production", got)
	}
	if net.badResponses(liar) != 0 {
		t.Fatal("an empty answer is backed off, not scored")
	}
	if liar.askedCount() != 1 {
		t.Fatalf("liar asked %d times, want 1", liar.askedCount())
	}
	for _, v := range validators {
		if net.badResponses(v) != 0 {
			t.Fatal("honest validator penalised")
		}
	}

	// The network commits on to 240. A verified CommitQC names 240, above
	// every advertised height: the gate reports the proven gap until it is
	// closed, and the stored blocks are fetched regardless of stale claims.
	for _, v := range validators {
		v.mu.Lock()
		for _, b := range all[236:241] {
			v.chain[b.Number64().Uint64()] = b
		}
		v.mu.Unlock()
	}
	net.svc.catchUpInProgress.Store(true)
	net.svc.CatchUpTo(240)
	if got := net.svc.HeightBehindQuorum(3); got != 5 {
		t.Fatalf("gate gap from the verified target = %d, want 5", got)
	}
	net.svc.catchUpInProgress.Store(false)
	net.svc.CatchUpTo(240)
	if got := chain.headNumber(); got != 240 {
		t.Fatalf("head after the CommitQC target = %d, want 240", got)
	}
	if got := net.svc.HeightBehindQuorum(3); got != 0 {
		t.Fatalf("gate gap = %d, want 0", got)
	}
}
