// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
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
