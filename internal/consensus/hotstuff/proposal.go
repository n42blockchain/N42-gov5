// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Leader-side block proposal emission for HotStuff-2.
// onBlockReady is invoked when the local node (as leader for the
// current view) has a block hash and tx root ready to broadcast.
// Guards non-leaders via IsLeader against the validator set and
// wires the proposal into the engine output channel under the
// current view number.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// onBlockReady is called when this node (as leader) has a block ready to propose.
func (e *ConsensusEngine) onBlockReady(blockHash types.Hash, txRootHash types.Hash) error {
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
		View:       view,
		BlockHash:  blockHash,
		JustifyQC:  justifyQC,
		Proposer:   e.myIndex,
		Signature:  signature.Marshal(),
		PrepareQC:  piggybacked,
		TxRootHash: txRootHash,
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

	// Tail-fork detection: if the proposal's JustifyQC skips a view where
	// we have a QC (the previous leader produced a valid block but the
	// current leader is ignoring it), log a warning. A full Carry protocol
	// mitigation would require validators to send recent votes to the
	// incoming leader, but detection alone is valuable for monitoring.
	if proposal.JustifyQC.View+1 < view && e.roundState.LockedQC().View == view-1 {
		log.Warn("potential tail-fork: proposal skips previous view QC",
			"view", view, "justifyQC.view", proposal.JustifyQC.View,
			"lockedQC.view", e.roundState.LockedQC().View,
			"proposer", proposal.Proposer)
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

	// Baby Raptr: track data availability commitment for post-import verification.
	// The TxRootHash is included in the proposal so that validators can verify
	// transaction data availability after block import.
	if proposal.TxRootHash != (types.Hash{}) && len(e.pendingTxRoots) < MaxImportedBlocks {
		e.pendingTxRoots[proposal.BlockHash] = proposal.TxRootHash
	}

	e.roundState.EnterVoting()
	now := time.Now()
	e.viewTiming.ProposalReceived = &now

	// Request block execution.
	if err := e.emit(EngineOutput{Type: OutputExecuteBlock, Hash: proposal.BlockHash}); err != nil {
		return err
	}

	// Double-vote prevention: only send one Round 1 vote per view.
	if e.roundState.HasVotedInView(view) {
		log.Debug("suppressing duplicate prepare vote", "view", view)
		return nil
	}

	// Import-gated voting (NOT optimistic): vote only once the block is imported
	// locally, so a CommitQC proves a quorum actually holds the block — not just
	// its hash. This couples view progress to block propagation; the head can only
	// advance as fast as blocks reach a quorum, which is what stops the engine
	// from spinning thousands of views ahead of the chain. If the block is already
	// imported (a direct push arrived before the Proposal), vote now; otherwise
	// defer until EventBlockImported fires onBlockImported, which casts the vote.
	e.pendingProposals[view] = proposal.BlockHash
	if e.importedBlocks[proposal.BlockHash] {
		log.Info("import-gated vote: block already imported, voting now", "view", view, "blockHash", proposal.BlockHash)
		e.roundState.RecordVote(view, proposal.BlockHash)
		return e.sendVote(view, proposal.BlockHash)
	}
	log.Info("import-gated vote: deferring until block imported",
		"view", view, "blockHash", proposal.BlockHash)
	return nil
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

	// Double-vote prevention: only send one Round 2 commit vote per view.
	if e.roundState.HasCommitVotedInView(view) {
		log.Debug("suppressing duplicate commit vote", "view", view)
		return nil
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
		HighTC:    e.roundState.HighestTC(),
	}

	now := time.Now()
	e.viewTiming.CommitVoteSent = &now
	e.roundState.RecordCommitVote(view) // record before send to prevent re-attempt

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
		HighTC:    e.roundState.HighestTC(),
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

// onBlockImported handles the BlockImported event and verifies DA commitment.
func (e *ConsensusEngine) onBlockImported(blockHash types.Hash, actualTxRoot types.Hash) error {
	if len(e.importedBlocks) < MaxImportedBlocks {
		e.importedBlocks[blockHash] = true
	}

	// Baby Raptr DA verification: compare the proposal's TxRootHash with
	// the actual transaction root computed during block import.
	if expectedTxRoot, ok := e.pendingTxRoots[blockHash]; ok {
		delete(e.pendingTxRoots, blockHash)
		if actualTxRoot != (types.Hash{}) && expectedTxRoot != actualTxRoot {
			log.Warn("DA verification failed: TxRootHash mismatch",
				"blockHash", blockHash,
				"expected", expectedTxRoot,
				"actual", actualTxRoot,
			)
			return &DAVerificationError{
				BlockHash:    blockHash,
				ExpectedRoot: expectedTxRoot,
				ActualRoot:   actualTxRoot,
			}
		}
		log.Debug("DA verification passed", "blockHash", blockHash, "txRoot", expectedTxRoot)
	}

	// Import-gated voting: now that this block is imported, cast the deferred
	// prepare vote if it is the block we are waiting to vote on in the current
	// view (recorded by processProposal). This is what advances the round once
	// the block has actually propagated to and been imported by us.
	view := e.roundState.CurrentView()
	if pending, ok := e.pendingProposals[view]; ok && pending == blockHash &&
		!e.roundState.HasVotedInView(view) {
		e.roundState.RecordVote(view, blockHash)
		log.Info("import-gated vote: casting deferred vote after import", "view", view, "blockHash", blockHash)
		return e.sendVote(view, blockHash)
	}
	log.Info("import-gated vote: block imported but no matching pending proposal", "view", view, "blockHash", blockHash, "hasPending", e.pendingProposals[view])

	return nil
}

