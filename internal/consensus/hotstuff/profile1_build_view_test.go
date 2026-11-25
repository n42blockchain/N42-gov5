// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// TestProfile1BuildViewNamesTheProposalView: BuildView is the current view
// for its leader, the next view for the next view's leader (a speculative
// build), and the current view for any other node.
func TestProfile1BuildViewNamesTheProposalView(t *testing.T) {
	setup := newTestSetup(t, 4)
	probe, _ := newP1Engine(t, setup, 0)
	v := probe.CurrentView()
	cur := int(LeaderForViewWithTenure(v, setup.vs, probe.leaderTenure))
	next := int(LeaderForViewWithTenure(v+1, setup.vs, probe.leaderTenure))
	if cur == next {
		t.Fatalf("test needs distinct leaders for views %d and %d", v, v+1)
	}
	for i := 0; i < 4; i++ {
		e, _ := newP1Engine(t, setup, i)
		want := v
		if i == next {
			want = v + 1
		}
		if got := e.BuildView(); got != want {
			t.Errorf("node %d: BuildView = %d, want %d (current %d, leader %d, next leader %d)", i, got, want, v, cur, next)
		}
	}
}

// TestProfile1PrepareStampsTheProposalView: on profile 1 a block built by the
// next view's leader (the speculative build during the previous view) carries
// the view it will be proposed in, so its coinbase is that view's leader. The
// 4-node smoke refused every such block ("coinbase is not the leader of the
// view") because Prepare stamped the current view.
func TestProfile1PrepareStampsTheProposalView(t *testing.T) {
	f := newP1Fixture(t)
	parent := f.chainOf(1)[1]
	setup := newTestSetup(t, 4)
	probe, _ := newP1Engine(t, setup, 0)
	v := probe.CurrentView()
	next := int(LeaderForViewWithTenure(v+1, setup.vs, probe.leaderTenure))
	e, _ := newP1Engine(t, setup, next)
	f.hs.lock.Lock()
	f.hs.engine = e
	f.hs.lock.Unlock()

	h := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(2)}
	if err := f.hs.Prepare(f.chain, h); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractViewFromExtra(h.Extra)
	if err != nil {
		t.Fatal(err)
	}
	if got != v+1 {
		t.Fatalf("speculative build stamped view %d, want the proposal view %d", got, v+1)
	}
}

// TestProfile1PrepareLinksParentCommitteeEvidence: with the committee pool
// wired (mandatory on profile 1, A19) Prepare stamps parentBeaconRoot =
// Blake3 of the parent's committee evidence (B5), and VerifyHeader accepts
// the sealed result. The profile-1 branch of Prepare skipped the link, so
// every follower refused block 2 ("committee-evidence link broken").
func TestProfile1PrepareLinksParentCommitteeEvidence(t *testing.T) {
	f := newP1Fixture(t)
	pool := newTestPool(t)
	f.hs.SetCommitteeEvidence(pool, nil)
	// Block 1 extends genesis: no parent evidence, zero root.
	h1 := f.child(f.genesis, 1, nil)
	h1.ParentBeaconRoot = nil
	if err := f.hs.Prepare(f.chain, h1); err != nil {
		t.Fatal(err)
	}
	if h1.ParentBeaconRoot == nil || *h1.ParentBeaconRoot != (types.Hash{}) {
		t.Fatalf("block 1 parentBeaconRoot = %v, want zero", h1.ParentBeaconRoot)
	}
	b1 := f.seal(f.child(f.genesis, 1, nil))
	f.chain.headers[1] = b1

	h2 := &block.Header{ParentHash: b1.Hash(), Number: uint256.NewInt(2)}
	if err := f.hs.Prepare(f.chain, h2); err != nil {
		t.Fatal(err)
	}
	ce, err := pool.BuildSimulatedCE(1, b1.Hash(), b1.ReceiptHash)
	if err != nil {
		t.Fatal(err)
	}
	want := ce.BeaconRoot()
	if h2.ParentBeaconRoot == nil || *h2.ParentBeaconRoot != want {
		t.Fatalf("block 2 parentBeaconRoot = %v, want Blake3(CE(block 1)) %s", h2.ParentBeaconRoot, want.Hex())
	}
	// A child carrying the stamped root passes the follower's check.
	c2 := f.child(b1, 2, nil)
	c2.ParentBeaconRoot = h2.ParentBeaconRoot
	if err := f.hs.VerifyHeader(f.chain, f.seal(c2), true); err != nil {
		t.Fatalf("VerifyHeader of the linked block 2: %v", err)
	}
}

// TestProfile1SpeculativeStampDoesNotPinTheProposal: the next view's leader
// builds speculatively during the previous view (clock T, right after the
// parent arrived), then pays the A17 wait and rebuilds in its own view at
// T+8. The rebuild must stamp floor(now) = T+8, not the speculative T. Mixed
// pre-run 2 (2026-11-25): gov5 v1 extending gov5 v0's block sealed at
// parent.time + 8 s but stamped parent.time (header deltas 0/16/8/8), since
// the speculative Prepare had fixed the (view, parent) stamp.
func TestProfile1SpeculativeStampDoesNotPinTheProposal(t *testing.T) {
	f := newP1Fixture(t)
	parent := f.chainOf(1)[1]
	setup := newTestSetup(t, 4)
	probe, _ := newP1Engine(t, setup, 0)
	v := probe.CurrentView()
	next := int(LeaderForViewWithTenure(v+1, setup.vs, probe.leaderTenure))
	if int(LeaderForViewWithTenure(v, setup.vs, probe.leaderTenure)) == next {
		t.Fatalf("test needs distinct leaders for views %d and %d", v, v+1)
	}
	e, _ := newP1Engine(t, setup, next)
	f.hs.lock.Lock()
	f.hs.engine = e
	f.hs.lock.Unlock()

	clock := time.Unix(int64(parent.Time), 300_000_000)
	f.hs.now = func() time.Time { return clock }
	prepare := func() *block.Header {
		h := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(2)}
		if err := f.hs.Prepare(f.chain, h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	spec := prepare()
	if spec.Time != parent.Time {
		t.Fatalf("speculative stamp = %d, want %d", spec.Time, parent.Time)
	}

	// The view turns to this node's, the A17 slot (8 s) elapses, the miner
	// discards the stale speculative block and rebuilds.
	e.mu.Lock()
	e.roundState.AdvanceView(v + 1)
	e.mu.Unlock()
	clock = clock.Add(8 * time.Second)
	real := prepare()
	if real.Time != parent.Time+8 {
		t.Fatalf("proposal stamp = %d (delta %d s), want parent.time + 8 = %d",
			real.Time, int64(real.Time)-int64(parent.Time), parent.Time+8)
	}
	// Within its own view the stamp stays fixed (one proposal per view, F6).
	clock = clock.Add(2 * time.Second)
	if again := prepare(); again.Time != real.Time {
		t.Fatalf("rebuild in the proposal view stamped %d, first stamp %d", again.Time, real.Time)
	}
}
