// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Vote buffering and batch verification for HotStuff-2.
// batchVerifyThreshold sets the minimum number of buffered votes
// before the engine attempts bls/blst batch verification; below the
// threshold votes are verified individually since batch setup cost
// outweighs the benefit. Feeds verified votes into VoteCollector.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/log"
)

// batchVerifyThreshold is the minimum number of buffered votes before batch
// verification is attempted. Below this threshold, votes are verified
// individually (batch overhead outweighs the benefit for very small sets).
const batchVerifyThreshold = 4

// pendingVote holds an unverified vote for batch BLS verification.
type pendingVote struct {
	voter    ValidatorIndex
	sigBytes []byte
	pk       common.PublicKey
	message  []byte
}

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

	// Equivocation detection. The tracker holds only VERIFIED votes (recorded in
	// flushPrepareVotes after batch verification), so a recorded prevHash is
	// authentic. A conflicting hash from the same voter is treated as
	// equivocation — and can produce slashing evidence — only after THIS vote's
	// signature is individually verified: the consensus gossip topic is
	// unsigned, so an attacker could otherwise inject two Vote payloads naming a
	// victim for two hashes and fabricate an equivocation. This individual
	// verify fires only on the rare conflicting-hash path; the normal
	// same-hash path stays on batch verification.
	if prevHash, exists := e.equivocationTracker[vote.Voter]; exists && prevHash != vote.BlockHash {
		pk, err := vs.GetPublicKey(vote.Voter)
		if err != nil {
			return err
		}
		if !VerifyBLSSignature(vote.Signature, pk, SigningMessage(view, vote.BlockHash)) {
			return &InvalidSignatureError{View: view, ValidatorIndex: vote.Voter}
		}
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

	// Check collector exists and block hash matches.
	if e.voteCollector == nil {
		return nil
	}
	if vote.BlockHash != e.voteCollector.BlockHash() {
		// A first-seen vote whose hash differs from the collector's is dropped
		// here (it never enters the QC), but must still be recorded — once
		// verified — so a LATER collector-hash vote from the same voter is
		// caught as equivocation regardless of arrival order. Verify
		// individually since this vote bypasses the batch path; a forged one is
		// dropped without polluting the tracker. Fires only on the anomalous
		// non-proposal-hash path.
		if _, exists := e.equivocationTracker[vote.Voter]; !exists {
			pk, err := vs.GetPublicKey(vote.Voter)
			if err != nil {
				return err
			}
			if !VerifyBLSSignature(vote.Signature, pk, SigningMessage(view, vote.BlockHash)) {
				return &InvalidSignatureError{View: view, ValidatorIndex: vote.Voter}
			}
			e.equivocationTracker[vote.Voter] = vote.BlockHash
		}
		return nil
	}

	pk, err := vs.GetPublicKey(vote.Voter)
	if err != nil {
		return err
	}
	msg := SigningMessage(view, vote.BlockHash)

	// Buffer vote for batch verification.
	e.prepareVoteBuf = append(e.prepareVoteBuf, pendingVote{
		voter:    vote.Voter,
		sigBytes: vote.Signature,
		pk:       pk,
		message:  msg,
	})

	// Flush batch when enough votes buffered or quorum reachable.
	pendingCount := len(e.prepareVoteBuf)
	existingCount := 0
	if e.voteCollector != nil {
		existingCount = e.voteCollector.VoteCount()
	}
	quorum := vs.QuorumSize()
	if pendingCount >= batchVerifyThreshold || (existingCount+pendingCount) >= quorum {
		if err := e.flushPrepareVotes(); err != nil {
			return err
		}
	}

	return e.tryFormPrepareQC()
}

// flushPrepareVotes batch-verifies all buffered Round 1 votes and adds valid
// ones to the collector. Falls back to individual verification on batch failure.
func (e *ConsensusEngine) flushPrepareVotes() error {
	if len(e.prepareVoteBuf) == 0 {
		return nil
	}
	buf := e.prepareVoteBuf
	e.prepareVoteBuf = e.prepareVoteBuf[:0] // reset

	valid := batchVerifyVotes(buf)
	// All buffered prepare votes share the collector's block hash (they passed
	// the block-hash gate in processVote). Record the first VERIFIED vote per
	// voter into the equivocation tracker here — so the tracker only ever holds
	// authentic hashes, and a later conflicting-hash vote is checked against a
	// real prior vote (see processVote).
	collectorHash := e.voteCollector.BlockHash()
	for _, pv := range valid {
		if _, exists := e.equivocationTracker[pv.voter]; !exists {
			e.equivocationTracker[pv.voter] = collectorHash
		}
		if err := e.voteCollector.AddVerifiedVoteFromBytes(pv.voter, pv.sigBytes); err != nil {
			if _, ok := err.(*DuplicateVoteError); ok {
				continue
			}
			return err
		}
	}
	return nil
}

// tryFormPrepareQC attempts to form a PrepareQC from collected Round 1 votes.
func (e *ConsensusEngine) tryFormPrepareQC() error {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()

	// Flush remaining buffered votes before checking quorum.
	if len(e.prepareVoteBuf) > 0 {
		if err := e.flushPrepareVotes(); err != nil {
			return err
		}
	}

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

	// CommitVote equivocation detection (mirrors processVote): the tracker holds
	// only VERIFIED commit votes (recorded in flushCommitVotes), and a
	// conflicting hash produces evidence only after THIS vote's signature is
	// individually verified — the gossip topic is unsigned, so an unverified
	// payload naming a victim must not fabricate an equivocation.
	if prevHash, exists := e.commitEquivocationTracker[cv.Voter]; exists && prevHash != cv.BlockHash {
		pk, err := vs.GetPublicKey(cv.Voter)
		if err != nil {
			return err
		}
		if !VerifyBLSSignature(cv.Signature, pk, CommitSigningMessage(view, cv.BlockHash)) {
			return &InvalidSignatureError{View: view, ValidatorIndex: cv.Voter}
		}
		log.Warn("commit-vote equivocation detected",
			"view", view, "validator", cv.Voter, "prevHash", prevHash, "newHash", cv.BlockHash)
		_ = e.emit(EngineOutput{
			Type:      OutputEquivocationDetected,
			View:      view,
			Validator: cv.Voter,
			Hash1:     prevHash,
			Hash2:     cv.BlockHash,
		})
		return nil // discard conflicting commit vote
	}

	if e.commitCollector == nil {
		return nil
	}
	if cv.BlockHash != e.commitCollector.BlockHash() {
		// Record a first-seen non-collector-hash commit vote (once verified) so
		// a later collector-hash vote from the same voter is caught as
		// equivocation regardless of order. Mirrors processVote.
		if _, exists := e.commitEquivocationTracker[cv.Voter]; !exists {
			pk, err := vs.GetPublicKey(cv.Voter)
			if err != nil {
				return err
			}
			if !VerifyBLSSignature(cv.Signature, pk, CommitSigningMessage(view, cv.BlockHash)) {
				return &InvalidSignatureError{View: view, ValidatorIndex: cv.Voter}
			}
			e.commitEquivocationTracker[cv.Voter] = cv.BlockHash
		}
		return nil
	}

	pk, err := vs.GetPublicKey(cv.Voter)
	if err != nil {
		return err
	}
	msg := CommitSigningMessage(view, cv.BlockHash)

	// Buffer vote for batch verification.
	e.commitVoteBuf = append(e.commitVoteBuf, pendingVote{
		voter:    cv.Voter,
		sigBytes: cv.Signature,
		pk:       pk,
		message:  msg,
	})

	// Flush batch when enough votes buffered or quorum reachable.
	pendingCount := len(e.commitVoteBuf)
	existingCount := 0
	if e.commitCollector != nil {
		existingCount = e.commitCollector.VoteCount()
	}
	quorum := vs.QuorumSize()
	if pendingCount >= batchVerifyThreshold || (existingCount+pendingCount) >= quorum {
		if err := e.flushCommitVotes(); err != nil {
			return err
		}
	}

	return e.tryFormCommitQC()
}

// flushCommitVotes batch-verifies all buffered Round 2 votes.
func (e *ConsensusEngine) flushCommitVotes() error {
	if len(e.commitVoteBuf) == 0 {
		return nil
	}
	buf := e.commitVoteBuf
	e.commitVoteBuf = e.commitVoteBuf[:0]

	valid := batchVerifyVotes(buf)
	collectorHash := e.commitCollector.BlockHash()
	for _, pv := range valid {
		if _, exists := e.commitEquivocationTracker[pv.voter]; !exists {
			e.commitEquivocationTracker[pv.voter] = collectorHash
		}
		if err := e.commitCollector.AddVerifiedVoteFromBytes(pv.voter, pv.sigBytes); err != nil {
			if _, ok := err.(*DuplicateVoteError); ok {
				continue
			}
			return err
		}
	}
	return nil
}

// tryFormCommitQC attempts to form a Commit QC from collected Round 2 votes.
func (e *ConsensusEngine) tryFormCommitQC() error {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()

	// Flush remaining buffered votes before checking quorum.
	if len(e.commitVoteBuf) > 0 {
		if err := e.flushCommitVotes(); err != nil {
			return err
		}
	}

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

// batchVerifyVotes verifies a batch of pending votes using BLS batch pairing.
// Returns only the votes with valid signatures. On batch failure, falls back
// to individual verification to isolate the invalid signature(s).
func batchVerifyVotes(votes []pendingVote) []pendingVote {
	if len(votes) == 0 {
		return nil
	}
	if len(votes) == 1 {
		// Single vote: individual verify (batch overhead not worth it).
		if VerifyBLSSignature(votes[0].sigBytes, votes[0].pk, votes[0].message) {
			return votes
		}
		log.Warn("BLS vote verification failed (single)", "voter", votes[0].voter)
		return nil
	}

	// Prepare batch arrays.
	sigs := make([][]byte, len(votes))
	msgs := make([][]byte, len(votes))
	pks := make([]common.PublicKey, len(votes))
	for i, v := range votes {
		sigs[i] = v.sigBytes
		msgs[i] = v.message
		pks[i] = v.pk
	}

	// Batch verify all at once.
	ok, err := blst.VerifyMultipleSignaturesVarMsg(sigs, msgs, pks)
	if err == nil && ok {
		return votes // All valid.
	}

	// Batch failed: fall back to individual verification to find bad votes.
	if err != nil {
		log.Warn("BLS batch verification error, falling back to individual", "err", err, "count", len(votes))
	} else {
		log.Warn("BLS batch verification failed, isolating invalid signatures", "count", len(votes))
	}
	valid := make([]pendingVote, 0, len(votes))
	for _, v := range votes {
		if VerifyBLSSignature(v.sigBytes, v.pk, v.message) {
			valid = append(valid, v)
		} else {
			log.Warn("BLS vote verification failed (individual fallback)", "voter", v.voter)
		}
	}
	return valid
}
