// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// lateProposalEngine is a node that moved to view 3 without seeing any
// proposal for view 1.
func lateProposalEngine(t *testing.T) (*testSetup, *ConsensusEngine, chan EngineOutput) {
	t.Helper()
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	e.mu.Lock()
	err := e.advanceToView(3)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(ch)
	return setup, e, ch
}

// AM34 item 6: both proposals of a double proposal arrive after the view
// closed, in either order; the pair is still proven, once.
func TestLateProposalPairBothOrders(t *testing.T) {
	a, b := types.Hash{0xAA}, types.Hash{0xBB}
	for _, order := range [][2]types.Hash{{a, b}, {b, a}} {
		setup, e, ch := lateProposalEngine(t)
		deliverProposal(t, e, eqProposal(e, setup, 1, order[0]))
		deliverProposal(t, e, eqProposal(e, setup, 1, order[0]))
		if ev := proposalEvidence(drainOutputs(ch)); len(ev) != 0 {
			t.Fatal("a single late proposal produced evidence")
		}
		deliverProposal(t, e, eqProposal(e, setup, 1, order[1]))
		checkProposalEvidence(t, setup, drainOutputs(ch), 1, order[0], order[1])
		if len(e.lateProposals) != 0 {
			t.Fatal("proven late proposal still kept")
		}
	}
}

// A forged first late proposal neither produces evidence nor blocks the real
// pair; a proposal from a non-leader is ignored.
func TestLateProposalForgedFirstAndWrongProposer(t *testing.T) {
	a, b := types.Hash{0xAA}, types.Hash{0xBB}
	setup, e, ch := lateProposalEngine(t)
	forged := eqProposal(e, setup, 1, a)
	forged.Signature = setup.keys[(forged.Proposer+1)%4].Sign(e.proposalSigningMessage(1, a)).Marshal()
	deliverProposal(t, e, forged)
	deliverProposal(t, e, eqProposal(e, setup, 1, b))
	if ev := proposalEvidence(drainOutputs(ch)); len(ev) != 0 {
		t.Fatal("forged late proposal produced evidence")
	}
	deliverProposal(t, e, eqProposal(e, setup, 1, a))
	checkProposalEvidence(t, setup, drainOutputs(ch), 1, b, a)

	other := eqProposal(e, setup, 2, a)
	other.Proposer = (other.Proposer + 1) % 4
	deliverProposal(t, e, other)
	if _, kept := e.lateProposals[2]; kept {
		t.Fatal("non-leader late proposal kept")
	}
}

// Late proposals are kept only inside the window, so memory stays bounded.
func TestLateProposalWindowBounded(t *testing.T) {
	setup, e, ch := lateProposalEngine(t)
	e.mu.Lock()
	err := e.advanceToView(3 + 3*lateProposalWindow)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	current := e.CurrentView()
	for v := ViewNumber(1); v < current; v++ {
		deliverProposal(t, e, eqProposal(e, setup, v, types.Hash{0xAA}))
	}
	if len(e.lateProposals) > lateProposalWindow {
		t.Fatalf("%d late proposals kept, window %d", len(e.lateProposals), lateProposalWindow)
	}
	deliverProposal(t, e, eqProposal(e, setup, 1, types.Hash{0xBB}))
	if ev := proposalEvidence(drainOutputs(ch)); len(ev) != 0 {
		t.Fatal("proposal beyond the window produced evidence")
	}
}
