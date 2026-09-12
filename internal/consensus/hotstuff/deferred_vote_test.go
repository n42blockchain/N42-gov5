// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// Under deferred execution a follower votes for a proposal once the block
// is checked (EventBlockChecked) AND its parent -- the JustifyQC block --
// is imported, in either order, without the block's own import.
func TestDeferredVoteAfterCheckAndParentImport(t *testing.T) {
	for _, checkedFirst := range []bool{true, false} {
		setup := newTestSetup(t, 4)
		follower, outputCh := newTestEngine(t, setup, 0)

		parent := types.Hash{0xA0}
		blockHash := types.Hash{0xAA}
		// The parent is the genesis-QC block for this test: pretend it was
		// imported, and make it the proposal's JustifyQC block.
		justify := GenesisQC()
		justify.BlockHash = parent
		msg := SigningMessage(1, blockHash)
		proposal := &Proposal{
			View:      1,
			BlockHash: blockHash,
			JustifyQC: justify,
			Proposer:  1,
			Signature: setup.keys[1].Sign(msg).Marshal(),
		}
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
			t.Fatalf("proposal: %v", err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
			t.Fatalf("checkedFirst=%v: voted before any check or import", checkedFirst)
		}
		first := ConsensusEvent{Type: EventBlockChecked, Hash: blockHash, ParentHash: parent}
		second := ConsensusEvent{Type: EventBlockImported, Hash: parent}
		if !checkedFirst {
			first, second = second, first
		}
		if err := follower.ProcessEvent(first); err != nil {
			t.Fatalf("first event: %v", err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
			t.Fatalf("checkedFirst=%v: voted after only one of check/parent-import", checkedFirst)
		}
		if err := follower.ProcessEvent(second); err != nil {
			t.Fatalf("second event: %v", err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 1 {
			t.Fatalf("checkedFirst=%v: expected 1 prepare vote after check + parent import, got %d", checkedFirst, votes)
		}
		// The block's own import must not produce a second vote.
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: blockHash, ParentHash: parent}); err != nil {
			t.Fatalf("own import: %v", err)
		}
		if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
			t.Fatalf("checkedFirst=%v: duplicate vote on the block's own import", checkedFirst)
		}
	}
}

// A checked block whose parent is NOT the JustifyQC block is refused.
func TestDeferredVoteRefusesWrongParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	justify := GenesisQC()
	justify.BlockHash = types.Hash{0xA0}
	blockHash := types.Hash{0xAA}
	proposal := &Proposal{View: 1, BlockHash: blockHash, JustifyQC: justify, Proposer: 1, Signature: setup.keys[1].Sign(SigningMessage(1, blockHash)).Marshal()}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	_ = follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: types.Hash{0xA0}})
	_ = follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: blockHash, ParentHash: types.Hash{0xB0}})
	if votes := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgVote); votes != 0 {
		t.Fatalf("voted for a checked block that does not extend the JustifyQC block")
	}
}
