// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// onBlockReady is called when this node (as leader) has a block ready to propose.
func (e *ConsensusEngine) onBlockReady(blockHash types.Hash) error {
	view := e.roundState.CurrentView()

	if !IsLeader(e.myIndex, view, e.validatorSet()) {
		return nil
	}

	if e.roundState.Phase() != PhaseWaitingForProposal {
		return nil
	}

	justifyQC := e.roundState.LockedQC().Clone()
	message := SigningMessage(view, blockHash)
	signature := e.secretKey.Sign(message)
	piggybacked := e.previousPrepareQC
	e.previousPrepareQC = nil

	proposal := &Proposal{
		View:      view,
		BlockHash: blockHash,
		JustifyQC: justifyQC,
		Proposer:  e.myIndex,
		Signature: signature.Marshal(),
		PrepareQC: piggybacked,
	}

	vs := e.validatorSet()
	e.voteCollector = NewVoteCollector(view, blockHash, vs.Len())
	e.commitCollector = NewVoteCollector(view, blockHash, vs.Len())
	e.roundState.EnterVoting()

	// Leader self-votes (GossipSub doesn't deliver back to sender).
	leaderSig := e.secretKey.Sign(message)
	if e.voteCollector != nil {
		_ = e.voteCollector.AddVote(e.myIndex, leaderSig)
	}

	now := time.Now()
	e.viewTiming.ProposalSent = &now

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgProposal,
			Payload: proposal,
		},
	}); err != nil {
		return err
	}

	// Check if quorum already reached (single-validator scenario).
	return e.tryFormPrepareQC()
}

// processProposal processes a proposal from the leader.
func (e *ConsensusEngine) processProposal(proposal *Proposal) error {
	view := e.roundState.CurrentView()

	if proposal.View != view {
		return &ViewMismatchError{Current: view, Received: proposal.View}
	}

	expectedLeader := LeaderForView(view, e.validatorSet())
	if proposal.Proposer != expectedLeader {
		return &InvalidProposerError{View: view, Expected: expectedLeader, Actual: proposal.Proposer}
	}

	// Verify proposer's BLS signature.
	pk, err := e.validatorSet().GetPublicKey(proposal.Proposer)
	if err != nil {
		return err
	}
	msg := SigningMessage(view, proposal.BlockHash)
	if !VerifyBLSSignature(proposal.Signature, pk, msg) {
		return &InvalidSignatureError{View: view, ValidatorIndex: proposal.Proposer}
	}

	// Verify justify_qc aggregate BLS signature (genesis QC is exempt).
	if proposal.JustifyQC.View > 0 {
		if vErr := VerifyQCAnyDomain(&proposal.JustifyQC, e.validatorSet()); vErr != nil {
			log.Warn("rejecting proposal with invalid justify_qc",
				"view", view, "proposer", proposal.Proposer, "err", vErr)
			return vErr
		}
	}

	// HotStuff-2 safety rule.
	if !e.roundState.IsSafeToVote(&proposal.JustifyQC) {
		return &SafetyViolationError{
			QCView:     proposal.JustifyQC.View,
			LockedView: e.roundState.LockedQC().View,
		}
	}

	e.roundState.UpdateLockedQC(&proposal.JustifyQC)

	// Process piggybacked PrepareQC (chained mode).
	if proposal.PrepareQC != nil {
		if vErr := VerifyQC(proposal.PrepareQC, e.validatorSet()); vErr == nil {
			e.roundState.UpdateLockedQC(proposal.PrepareQC)
		} else {
			log.Warn("rejected invalid piggybacked PrepareQC", "view", view, "err", vErr)
		}
	}

	e.roundState.EnterVoting()
	now := time.Now()
	e.viewTiming.ProposalReceived = &now

	// Request block execution.
	if err := e.emit(EngineOutput{Type: OutputExecuteBlock, Hash: proposal.BlockHash}); err != nil {
		return err
	}

	// Optimistic Voting: vote immediately after proposal validation.
	log.Info("optimistic vote: voting immediately after proposal validation",
		"view", view, "blockHash", proposal.BlockHash)
	return e.sendVote(view, proposal.BlockHash)
}

// processPrepareQC processes a PrepareQC from the leader.
func (e *ConsensusEngine) processPrepareQC(pqc *PrepareQCMsg) error {
	view := e.roundState.CurrentView()

	if pqc.View != view {
		return &ViewMismatchError{Current: view, Received: pqc.View}
	}

	if err := VerifyQC(&pqc.QC, e.validatorSet()); err != nil {
		return err
	}

	e.roundState.UpdateLockedQC(&pqc.QC)
	e.roundState.EnterPreCommit()

	// Send CommitVote (Round 2).
	commitMsg := CommitSigningMessage(view, pqc.BlockHash)
	commitSig := e.secretKey.Sign(commitMsg)
	leader := LeaderForView(view, e.validatorSet())

	commitVote := &CommitVote{
		View:      view,
		BlockHash: pqc.BlockHash,
		Voter:     e.myIndex,
		Signature: commitSig.Marshal(),
	}

	now := time.Now()
	e.viewTiming.CommitVoteSent = &now

	return e.emit(EngineOutput{
		Type:   OutputSendToValidator,
		Target: leader,
		Message: &ConsensusMsg{
			Type:    MsgCommitVote,
			Payload: commitVote,
		},
	})
}

// sendVote sends a Round 1 vote for the given view and block hash.
func (e *ConsensusEngine) sendVote(view ViewNumber, blockHash types.Hash) error {
	leader := LeaderForView(view, e.validatorSet())
	voteMsg := SigningMessage(view, blockHash)
	voteSig := e.secretKey.Sign(voteMsg)

	vote := &Vote{
		View:      view,
		BlockHash: blockHash,
		Voter:     e.myIndex,
		Signature: voteSig.Marshal(),
	}

	now := time.Now()
	e.viewTiming.VoteSent = &now

	return e.emit(EngineOutput{
		Type:   OutputSendToValidator,
		Target: leader,
		Message: &ConsensusMsg{
			Type:    MsgVote,
			Payload: vote,
		},
	})
}

// onBlockImported handles the BlockImported event.
func (e *ConsensusEngine) onBlockImported(blockHash types.Hash) error {
	if len(e.importedBlocks) < MaxImportedBlocks {
		e.importedBlocks[blockHash] = true
	}
	return nil
}

