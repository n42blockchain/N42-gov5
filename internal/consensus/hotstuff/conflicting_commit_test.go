// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node stored",
// round 35zzzg, 2026-09-21): a follower running two-phase (deferred-execution)
// voting cast BOTH a prepare vote and a commit vote for a proposal that did
// not extend the block it had just committed one view earlier -- the same
// height, the same (grandparent) parent, but a different block, proposed by
// the same leader in the very next view with a JustifyQC that legitimately
// pointed at the block it superseded.
//
// The reason: e.twoPhaseVote's Round 1 branch in processProposal votes
// immediately, gated only on "is the block already imported" (line ~257);
// when it is not (the overwhelmingly common case -- a Proposal always
// arrives before the deferred-execution check or the full import completes)
// extendsJustify is never even called. Round 2 (processPrepareQC) has no
// extends check at all: deferredAttested only asks whether the block's own
// (possibly stale) parent is locally applied, which is true whenever that
// parent is old enough to already be canonical -- exactly the case for a
// stale sibling. Both gaps must close for the vote rule to be sound; a
// leader-side guard alone (worker.go's firstSealedOnParent) is not
// sufficient because a correct vote rule makes a stale proposal harmless
// even if the leader still emits one.
//
// These tests replay that shape at the engine level: a proposal in view 1
// whose JustifyQC names a block ("committed", height h) the proposal itself
// does not extend (its real, checked/imported parent is a different, older
// block at height h-1). No prepare vote and no commit vote must ever be
// sent for it.

// twoPhaseFollower returns a 4-validator follower engine (this node index 0)
// with two-phase (deferred-execution) voting enabled, matching how the fleet
// runs it.
func twoPhaseFollower(t *testing.T) (*ConsensusEngine, chan EngineOutput, *testSetup) {
	t.Helper()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)
	return follower, outputCh, setup
}

// signedProposal builds a validly-signed Proposal from validator 1 (never
// this test's own index 0, matching a follower receiving someone else's
// proposal) for view/blockHash, carrying justifyQC.
func signedProposal(setup *testSetup, view ViewNumber, blockHash types.Hash, justify QuorumCertificate) *Proposal {
	msg := SigningMessage(view, blockHash)
	return &Proposal{
		View:      view,
		BlockHash: blockHash,
		JustifyQC: justify,
		Proposer:  1,
		Signature: setup.keys[1].Sign(msg).Marshal(),
	}
}

// TestTwoPhasePrepareVoteRefusesNonExtendingProposal is PART 2's Round-1
// regression: it MUST fail on today's code (a prepare vote is sent the
// instant the Proposal arrives, before this node has any way to know the
// block's real parent) and MUST pass once processProposal's two-phase branch
// defers to the same checked/imported-gated wait the non-two-phase path
// already uses.
func TestTwoPhasePrepareVoteRefusesNonExtendingProposal(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	committed := types.Hash{0xC1}  // the block this replica's justify names (the view-v commit)
	staleParent := types.Hash{0xB1} // the proposal's REAL parent -- an older, different block
	blockHash := types.Hash{0xAA}   // the proposal itself (a stale sibling of `committed`)

	justify := GenesisQC()
	justify.BlockHash = committed

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}

	// The block's real parent is not known yet (no EventBlockChecked /
	// EventBlockImported has fired) -- a sound Round 1 must not vote on
	// nothing but the Proposal message itself.
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("prepare-voted for a proposal before its parent was known: %d votes", votes)
	}

	// The parent arrives and it does NOT match the JustifyQC block: this is
	// exactly the extends-rule violation. No vote must ever be sent for it.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: staleParent}); err != nil {
		t.Fatalf("EventBlockImported(staleParent): %v", err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: staleParent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("prepare-voted for a proposal that does not extend its JustifyQC block: %d votes", votes)
	}
}

// TestTwoPhaseCommitVoteRefusesNonExtendingProposal is PART 2's Round-2
// regression: even granting that a (buggy or Byzantine) PrepareQC formed for
// a non-extending block, this replica must still refuse the commit vote.
// MUST fail on today's code (processPrepareQC / deferredAttested never call
// extendsJustify) and MUST pass once the fix adds that check.
func TestTwoPhaseCommitVoteRefusesNonExtendingProposal(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	committed := types.Hash{0xC2}
	staleParent := types.Hash{0xB2}
	blockHash := types.Hash{0xAB}

	justify := GenesisQC()
	justify.BlockHash = committed

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	drainOutputs(outputCh) // Round 1's outcome is TestTwoPhasePrepareVoteRefusesNonExtendingProposal's concern.

	// The block's real (stale) parent is old enough to already be applied
	// everywhere -- exactly what makes deferredAttested trivially true for a
	// stale sibling.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: staleParent}); err != nil {
		t.Fatalf("EventBlockImported(staleParent): %v", err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: staleParent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	drainOutputs(outputCh)

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); votes != 0 {
		t.Fatalf("commit-voted for a proposal that does not extend its JustifyQC block: %d votes", votes)
	}
}

// TestTwoPhaseVotesStillFireForAnExtendingProposal is the happy-path guard:
// the fix must not turn two-phase voting into "never vote". A proposal whose
// checked parent IS the JustifyQC block still gets both votes, in the same
// checked/imported order flexibility the non-two-phase path already allows.
func TestTwoPhaseVotesStillFireForAnExtendingProposal(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	parent := types.Hash{0xC3}
	blockHash := types.Hash{0xAC}

	justify := GenesisQC()
	justify.BlockHash = parent

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: parent}); err != nil {
		t.Fatalf("EventBlockImported(parent): %v", err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
		t.Fatalf("expected exactly 1 prepare vote for an honest, extending proposal, got %d", votes)
	}

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); votes != 1 {
		t.Fatalf("expected exactly 1 commit vote for an honest, extending proposal, got %d", votes)
	}
}
