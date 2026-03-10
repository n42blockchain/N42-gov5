// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/log"
)

// processVote processes a Round 1 (Prepare) vote from a validator.
func (e *ConsensusEngine) processVote(vote *Vote) error {
	view := e.roundState.CurrentView()

	if vote.View != view {
		return &ViewMismatchError{Current: view, Received: vote.View}
	}

	if !IsLeader(e.myIndex, view, e.validatorSet()) {
		return nil
	}

	// Validate voter index is within the known validator set.
	vs := e.validatorSet()
	if _, err := vs.GetPublicKey(vote.Voter); err != nil {
		return err
	}

	// Equivocation detection.
	if prevHash, exists := e.equivocationTracker[vote.Voter]; exists {
		if prevHash != vote.BlockHash {
			log.Warn("equivocation detected: validator voted for two different blocks",
				"view", view, "validator", vote.Voter, "prevHash", prevHash, "newHash", vote.BlockHash)
			_ = e.emit(EngineOutput{
				Type:      OutputEquivocationDetected,
				View:      view,
				Validator: vote.Voter,
				Hash1:     prevHash,
				Hash2:     vote.BlockHash,
			})
			return nil
		}
	} else {
		e.equivocationTracker[vote.Voter] = vote.BlockHash
	}

	// Check collector exists and block hash matches.
	if e.voteCollector == nil {
		return nil
	}
	if vote.BlockHash != e.voteCollector.BlockHash() {
		return nil
	}

	// Verify BLS signature (validator bounds already checked above).
	pk, err := vs.GetPublicKey(vote.Voter)
	if err != nil {
		return err
	}
	msg := SigningMessage(view, vote.BlockHash)
	if !VerifyBLSSignature(vote.Signature, pk, msg) {
		return &InvalidSignatureError{View: view, ValidatorIndex: vote.Voter}
	}

	// Add verified vote from raw bytes.
	if err := e.voteCollector.AddVerifiedVoteFromBytes(vote.Voter, vote.Signature); err != nil {
		if _, ok := err.(*DuplicateVoteError); ok {
			return nil
		}
		return err
	}

	return e.tryFormPrepareQC()
}

// tryFormPrepareQC attempts to form a PrepareQC from collected Round 1 votes.
func (e *ConsensusEngine) tryFormPrepareQC() error {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()

	if e.voteCollector == nil || !e.voteCollector.HasQuorum(vs.QuorumSize()) || e.prepareQC != nil {
		return nil
	}

	qc, err := e.voteCollector.BuildQC(vs)
	if err != nil {
		return err
	}

	now := time.Now()
	e.viewTiming.PrepareQCFormed = &now
	e.viewTiming.PrepareVoteCount = uint32(e.voteCollector.VoteCount())
	e.prepareQC = qc
	e.roundState.EnterPreCommit()
	e.roundState.UpdateLockedQC(qc)

	blockHash := qc.BlockHash
	prepareQCMsg := &PrepareQCMsg{View: view, BlockHash: blockHash, QC: qc.Clone()}

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgPrepareQC,
			Payload: prepareQCMsg,
		},
	}); err != nil {
		return err
	}

	// Leader self-vote for CommitVote (Round 2).
	commitMsg := CommitSigningMessage(view, blockHash)
	commitSig := e.secretKey.Sign(commitMsg)
	if e.commitCollector != nil {
		_ = e.commitCollector.AddVote(e.myIndex, commitSig)
	}

	return e.tryFormCommitQC()
}

// processCommitVote processes a Round 2 (Commit) vote from a validator.
func (e *ConsensusEngine) processCommitVote(cv *CommitVote) error {
	view := e.roundState.CurrentView()

	if cv.View != view {
		return &ViewMismatchError{Current: view, Received: cv.View}
	}

	if !IsLeader(e.myIndex, view, e.validatorSet()) {
		return nil
	}

	// Validate voter index is within the known validator set.
	vs := e.validatorSet()
	if _, err := vs.GetPublicKey(cv.Voter); err != nil {
		return err
	}

	if e.commitCollector == nil {
		return nil
	}
	if cv.BlockHash != e.commitCollector.BlockHash() {
		return nil
	}

	// Verify BLS signature (validator bounds already checked above).
	pk, err := vs.GetPublicKey(cv.Voter)
	if err != nil {
		return err
	}
	msg := CommitSigningMessage(view, cv.BlockHash)
	if !VerifyBLSSignature(cv.Signature, pk, msg) {
		return &InvalidSignatureError{View: view, ValidatorIndex: cv.Voter}
	}

	if err := e.commitCollector.AddVerifiedVoteFromBytes(cv.Voter, cv.Signature); err != nil {
		if _, ok := err.(*DuplicateVoteError); ok {
			return nil
		}
		return err
	}

	return e.tryFormCommitQC()
}

// tryFormCommitQC attempts to form a Commit QC from collected Round 2 votes.
func (e *ConsensusEngine) tryFormCommitQC() error {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()

	if e.commitCollector == nil || !e.commitCollector.HasQuorum(vs.QuorumSize()) {
		return nil
	}

	blockHash := e.commitCollector.BlockHash()
	commitMsg := CommitSigningMessage(view, blockHash)
	commitQC, err := e.commitCollector.BuildQCWithMessage(vs, commitMsg)
	if err != nil {
		return err
	}

	commitVoteCount := e.commitCollector.VoteCount()
	now := time.Now()
	e.viewTiming.CommitQCFormed = &now
	e.viewTiming.CommitVoteCount = uint32(commitVoteCount)

	log.Info("block committed!", "view", view, "blockHash", blockHash)

	e.roundState.Commit(commitQC.Clone())

	// Broadcast Decide.
	decide := &Decide{
		View:      view,
		BlockHash: blockHash,
		CommitQC:  commitQC.Clone(),
	}
	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgDecide,
			Payload: decide,
		},
	}); err != nil {
		return err
	}

	// Emit BlockCommitted.
	if err := e.emit(EngineOutput{
		Type: OutputBlockCommitted,
		View: view,
		Hash: blockHash,
		QC:   commitQC,
	}); err != nil {
		return err
	}

	nextView := view + 1
	if err := e.advanceToView(nextView); err != nil {
		return err
	}

	actualView := e.roundState.CurrentView()
	return e.emit(EngineOutput{Type: OutputViewChanged, View: actualView})
}
