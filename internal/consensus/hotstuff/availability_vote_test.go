// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func newAvailabilityHarness(t *testing.T, n int, genesis types.Hash, availability bool) *chaosHarness {
	t.Helper()
	h := newChaosHarness(t, n)
	for _, e := range h.engines {
		e.SetTwoPhaseVote(true)
		e.SetVoteOnAvailability(availability)
	}
	h.markBlockImported(genesis)
	for i := range h.channels {
		drainOutputs(h.channels[i])
	}
	return h
}

// lagExec models each node's execution pipeline under deferred depth 2: a
// block is executed (EventBlockImported) only two views after it was
// proposed, and a leader may propose height N only once it executed N-2.
type lagExec struct {
	executed map[types.Hash]bool
	chain    []types.Hash // proposed blocks in height order (index = height-1)
}

func (l *lagExec) executedHeight() int {
	n := 0
	for i, b := range l.chain {
		if l.executed[b] {
			n = i + 1
		}
	}
	return n
}

// TestAvailabilityVoteFourNodesExecutionLagsTwo (P2a, items b and c): four
// in-process engines with vote on availability commit eight consecutive
// views while no node has executed the block it votes for or its parent;
// execution runs two blocks behind consensus, and every proposal is made by
// a leader that has executed height N-2 (the lag gate), never more than two
// behind.
func TestAvailabilityVoteFourNodesExecutionLagsTwo(t *testing.T) {
	genesis := types.Hash{0x70}
	h := newAvailabilityHarness(t, 4, genesis, true)
	le := &lagExec{executed: map[types.Hash]bool{genesis: true}}

	parent := genesis
	for view := ViewNumber(1); view <= 8; view++ {
		height := int(view)
		// Lag gate: the leader of height N has executed N-2.
		if got := le.executedHeight(); got < height-2 {
			t.Fatalf("view %d: leader proposes height %d having executed only %d", view, height, got)
		}
		blk := blockHashForView(view)
		// Neither the block nor its parent (for view >= 2) is executed
		// anywhere when the vote runs.
		if le.executed[blk] || (view >= 2 && le.executed[parent]) {
			t.Fatalf("view %d: test setup executed too early", view)
		}
		if !deferredRound(t, h, view, blk, parent, true) {
			t.Fatalf("view %d did not commit on availability alone", view)
		}
		le.chain = append(le.chain, blk)
		// Execution runs behind: height N-1 executes after N commits, so
		// at the next proposal (N+1) the executed height is N-1.
		if height >= 2 {
			done := le.chain[height-2]
			h.markBlockImported(done)
			le.executed[done] = true
		}
		for i := range h.channels {
			drainOutputs(h.channels[i])
		}
		if lag := height - le.executedHeight(); lag > 2 {
			t.Fatalf("view %d: execution lags %d blocks", view, lag)
		}
		parent = blk
	}
}

// TestWithoutAvailabilityVoteTheParentImportGates: the same rounds without
// the switch stall at the first view whose parent is not executed.
func TestWithoutAvailabilityVoteTheParentImportGates(t *testing.T) {
	genesis := types.Hash{0x71}
	h := newAvailabilityHarness(t, 4, genesis, false)
	b1 := blockHashForView(1)
	if !deferredRound(t, h, 1, b1, genesis, true) {
		t.Fatal("view 1 (parent = genesis, imported) must commit")
	}
	for i := range h.channels {
		drainOutputs(h.channels[i])
	}
	if deferredRound(t, h, 2, blockHashForView(2), b1, true) {
		t.Fatal("view 2 committed with its parent unexecuted and the availability vote off")
	}
}
