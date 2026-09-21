// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// S31 (docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh): the prepare vote may fire on a
// block's HEADER alone -- EventBlockHeaderKnown, emitted well before
// CheckDeferredBlock's own per-transaction walk -- because extendsJustify
// only ever reads the parent hash, a header field. These tests replay the
// four shapes the task named: a non-extending header refused before any
// deferred check runs; a header for the WRONG block hash never unlocking the
// vote; both delivery orders (header before/after the Proposal) voting
// exactly once; and, with no header event at all, the existing S26
// checked/imported fallback still carrying the vote.

// TestHeaderVoteRefusesNonExtendingHeaderBeforeAnyDeferredCheck: PART of
// S26's own regression stays enforced, now reachable via the EARLIER path
// too -- and reachable WITHOUT ever delivering EventBlockChecked, proving
// the refusal does not depend on the deferred check having run.
func TestHeaderVoteRefusesNonExtendingHeaderBeforeAnyDeferredCheck(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	committed := types.Hash{0xD1}   // the block this replica's justify names
	staleParent := types.Hash{0xE1} // the pushed header's REAL parent -- does not extend committed
	blockHash := types.Hash{0xF1}

	justify := GenesisQC()
	justify.BlockHash = committed

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("voted before the header arrived: %d votes", votes)
	}

	// The header arrives (peeked on the block-push path) -- NO EventBlockChecked
	// is ever delivered in this test, so a vote here cannot be credited to the
	// deferred check.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: blockHash, ParentHash: staleParent, Number: 100}); err != nil {
		t.Fatalf("EventBlockHeaderKnown: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("prepare-voted on a header whose parent does not extend the JustifyQC block: %d votes", votes)
	}
}

// TestHeaderVoteIgnoresHeaderForADifferentBlockHash: a header event naming a
// DIFFERENT block hash than the one this view's Proposal expects must not
// unlock the vote for the expected block -- the map entry it populates never
// matches pendingProposals[view].
func TestHeaderVoteIgnoresHeaderForADifferentBlockHash(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	committed := types.Hash{0xD2}
	blockHash := types.Hash{0xF2}   // what the Proposal actually names
	otherHash := types.Hash{0xF3}   // a different block entirely

	justify := GenesisQC()
	justify.BlockHash = committed

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	drainOutputs(outputCh)

	// A header event for a DIFFERENT hash, even one whose parent legitimately
	// extends `committed`, must not vote for blockHash.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: otherHash, ParentHash: committed, Number: 100}); err != nil {
		t.Fatalf("EventBlockHeaderKnown(otherHash): %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("a header event for a different block hash unlocked this view's vote: %d votes", votes)
	}
}

// TestHeaderVoteFiresExactlyOnceRegardlessOfOrder: the body (and therefore
// the header peek) can beat the Proposal message, or arrive after it. Either
// order must vote exactly once for an honest, extending proposal.
func TestHeaderVoteFiresExactlyOnceRegardlessOfOrder(t *testing.T) {
	for _, headerFirst := range []bool{true, false} {
		follower, outputCh, setup := twoPhaseFollower(t)

		parent := types.Hash{0xD3}
		blockHash := types.Hash{0xF4}

		justify := GenesisQC()
		justify.BlockHash = parent

		proposal := signedProposal(setup, 1, blockHash, justify)
		headerEvent := ConsensusEvent{Type: EventBlockHeaderKnown, Hash: blockHash, ParentHash: parent, Number: 100}
		proposalEvent := ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}

		first, second := proposalEvent, headerEvent
		if headerFirst {
			first, second = headerEvent, proposalEvent
		}
		if err := follower.ProcessEvent(first); err != nil {
			t.Fatalf("headerFirst=%v first event: %v", headerFirst, err)
		}
		if err := follower.ProcessEvent(second); err != nil {
			t.Fatalf("headerFirst=%v second event: %v", headerFirst, err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
			t.Fatalf("headerFirst=%v: expected exactly 1 prepare vote, got %d", headerFirst, votes)
		}
	}
}

// TestHeaderVoteFallsBackToCheckedGateWithoutTheHeaderEvent: when the header
// event never arrives (e.g. the sync layer's notifier is not wired, or the
// peek failed), the vote must still fire via the existing S26 checked/
// imported gate -- never blind, and never stuck forever.
func TestHeaderVoteFallsBackToCheckedGateWithoutTheHeaderEvent(t *testing.T) {
	follower, outputCh, setup := twoPhaseFollower(t)

	parent := types.Hash{0xD4}
	blockHash := types.Hash{0xF5}

	justify := GenesisQC()
	justify.BlockHash = parent

	proposal := signedProposal(setup, 1, blockHash, justify)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("voted before either gate was satisfied: %d votes", votes)
	}

	// No EventBlockHeaderKnown anywhere in this test -- only the pre-existing
	// checked/imported path.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: parent}); err != nil {
		t.Fatalf("EventBlockImported(parent): %v", err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
		t.Fatalf("expected the checked/imported fallback to vote exactly once, got %d", votes)
	}
}
