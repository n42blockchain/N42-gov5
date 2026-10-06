// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestProcessCommitVote_ViewMismatch covers the view-mismatch guard.
func TestProcessCommitVote_ViewMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	leader := LeaderForView(1, setup.vs)
	e, _ := newTestEngine(t, setup, int(leader))

	err := e.processCommitVote(&CommitVote{View: 99, Voter: 0}, msgTiming{})
	var vme *ViewMismatchError
	if err == nil {
		t.Fatal("expected a ViewMismatchError")
	}
	if !asViewMismatch(err, &vme) {
		t.Fatalf("processCommitVote error = %v (%T), want *ViewMismatchError", err, err)
	}
}

func asViewMismatch(err error, target **ViewMismatchError) bool {
	if vme, ok := err.(*ViewMismatchError); ok {
		*target = vme
		return true
	}
	return false
}

// TestProcessCommitVote_NotLeaderIsNoop covers the non-leader early return:
// a follower silently ignores a commit vote (leader-only aggregation).
func TestProcessCommitVote_NotLeaderIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	view := ViewNumber(1)
	leader := LeaderForView(view, setup.vs)
	// Pick a non-leader index.
	var follower ValidatorIndex
	for i := ValidatorIndex(0); i < ValidatorIndex(len(setup.validators)); i++ {
		if i != leader {
			follower = i
			break
		}
	}
	e, _ := newTestEngine(t, setup, int(follower))

	if err := e.processCommitVote(&CommitVote{View: view, Voter: 0}, msgTiming{}); err != nil {
		t.Fatalf("non-leader processCommitVote = %v, want nil (ignored)", err)
	}
}

// TestProcessCommitVote_UnknownVoterIndexErrors covers the voter-index
// validation guard.
func TestProcessCommitVote_UnknownVoterIndexErrors(t *testing.T) {
	setup := newTestSetup(t, 4)
	view := ViewNumber(1)
	leader := LeaderForView(view, setup.vs)
	e, _ := newTestEngine(t, setup, int(leader))

	if err := e.processCommitVote(&CommitVote{View: view, Voter: 999}, msgTiming{}); err == nil {
		t.Fatal("expected an error for an out-of-range voter index")
	}
}

// TestProcessCommitVote_NilCollectorIsNoop covers the nil commitCollector
// guard (no Round 1 PrepareQC has formed yet for this view).
func TestProcessCommitVote_NilCollectorIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	view := ViewNumber(1)
	leader := LeaderForView(view, setup.vs)
	e, _ := newTestEngine(t, setup, int(leader))
	e.commitCollector = nil

	if err := e.processCommitVote(&CommitVote{View: view, Voter: 0, BlockHash: types.Hash{0x01}}, msgTiming{}); err != nil {
		t.Fatalf("processCommitVote with nil collector = %v, want nil", err)
	}
}

// TestProcessCommitVote_EquivocationDetected covers the equivocation branch:
// a second, correctly-signed commit vote from the same voter naming a
// different block triggers OutputEquivocationDetected.
func TestProcessCommitVote_EquivocationDetected(t *testing.T) {
	setup := newTestSetup(t, 4)
	view := ViewNumber(1)
	leader := LeaderForView(view, setup.vs)
	e, outputCh := newTestEngine(t, setup, int(leader))
	e.commitCollector = NewVoteCollector(view, types.Hash{0xaa}, setup.vs.Len())
	e.commitEquivocationTracker[1] = types.Hash{0x01} // previously-seen hash for voter 1

	newHash := types.Hash{0x02}
	sig := setup.keys[1].Sign(e.commitSigningMessage(view, newHash))
	cv := &CommitVote{View: view, Voter: 1, BlockHash: newHash, Signature: sig.Marshal()}

	if err := e.processCommitVote(cv, msgTiming{}); err != nil {
		t.Fatalf("processCommitVote: %v", err)
	}
	outputs := drainOutputs(outputCh)
	found := false
	for _, o := range outputs {
		if o.Type == OutputEquivocationDetected && o.Validator == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an OutputEquivocationDetected output")
	}
}
