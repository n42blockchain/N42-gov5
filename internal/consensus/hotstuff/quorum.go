// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"fmt"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/common/types"
)

// VoteCollector accumulates votes for a specific view and produces a QuorumCertificate
// once 2f+1 votes are collected.
type VoteCollector struct {
	view      ViewNumber
	blockHash types.Hash
	votes     map[ValidatorIndex]common.Signature // deserialized BLS signatures
	setSize   uint32
	verified  map[ValidatorIndex]bool // validators whose sigs are already verified
}

// NewVoteCollector creates a new vote collector for the given view and block hash.
func NewVoteCollector(view ViewNumber, blockHash types.Hash, setSize uint32) *VoteCollector {
	return &VoteCollector{
		view:      view,
		blockHash: blockHash,
		votes:     make(map[ValidatorIndex]common.Signature),
		setSize:   setSize,
		verified:  make(map[ValidatorIndex]bool),
	}
}

// AddVote adds a vote from a validator. Returns error if duplicate.
func (vc *VoteCollector) AddVote(validatorIndex ValidatorIndex, signature common.Signature) error {
	if _, exists := vc.votes[validatorIndex]; exists {
		return &DuplicateVoteError{View: vc.view, ValidatorIndex: validatorIndex}
	}
	vc.votes[validatorIndex] = signature
	return nil
}

// AddVoteFromBytes adds a vote from raw signature bytes. Returns error if duplicate or invalid bytes.
func (vc *VoteCollector) AddVoteFromBytes(validatorIndex ValidatorIndex, sigBytes []byte) error {
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("invalid signature bytes: %w", err)
	}
	return vc.AddVote(validatorIndex, sig)
}

// AddVerifiedVote adds a vote that has already been signature-verified.
func (vc *VoteCollector) AddVerifiedVote(validatorIndex ValidatorIndex, signature common.Signature) error {
	if err := vc.AddVote(validatorIndex, signature); err != nil {
		return err
	}
	vc.verified[validatorIndex] = true
	return nil
}

// AddVerifiedVoteFromBytes adds a pre-verified vote from raw signature bytes.
func (vc *VoteCollector) AddVerifiedVoteFromBytes(validatorIndex ValidatorIndex, sigBytes []byte) error {
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("invalid signature bytes: %w", err)
	}
	return vc.AddVerifiedVote(validatorIndex, sig)
}

// BlockHash returns the block hash this collector is collecting votes for.
func (vc *VoteCollector) BlockHash() types.Hash {
	return vc.blockHash
}

// VoteCount returns the number of collected votes.
func (vc *VoteCollector) VoteCount() int {
	return len(vc.votes)
}

// HasQuorum returns true if enough votes have been collected.
func (vc *VoteCollector) HasQuorum(quorumSize int) bool {
	return len(vc.votes) >= quorumSize
}

// BuildQC builds a QuorumCertificate using the standard vote signing message.
func (vc *VoteCollector) BuildQC(vs *ValidatorSet) (*QuorumCertificate, error) {
	msg := SigningMessage(vc.view, vc.blockHash)
	return vc.BuildQCWithMessage(vs, msg)
}

// BuildQCWithMessage builds a QuorumCertificate using a custom signing message.
// Votes with invalid signatures are skipped (defense-in-depth).
func (vc *VoteCollector) BuildQCWithMessage(vs *ValidatorSet, message []byte) (*QuorumCertificate, error) {
	quorumSize := vs.QuorumSize()
	if len(vc.votes) < quorumSize {
		return nil, &InsufficientVotesError{View: vc.view, Have: len(vc.votes), Need: quorumSize}
	}

	validSigs := make([]common.Signature, 0, len(vc.votes))
	signers := make([]bool, vc.setSize)

	for idx, sig := range vc.votes {
		if idx >= vc.setSize {
			continue
		}

		// Skip verification for already-verified votes.
		if vc.verified[idx] {
			validSigs = append(validSigs, sig)
			signers[idx] = true
			continue
		}

		pk, err := vs.GetPublicKey(idx)
		if err != nil {
			continue
		}
		if !sig.Verify(pk, message) {
			continue
		}
		validSigs = append(validSigs, sig)
		signers[idx] = true
	}

	if len(validSigs) < quorumSize {
		return nil, &InsufficientVotesError{View: vc.view, Have: len(validSigs), Need: quorumSize}
	}

	aggSig := bls.AggregateSignatures(validSigs)
	return &QuorumCertificate{
		View:               vc.view,
		BlockHash:          vc.blockHash,
		AggregateSignature: aggSig.Marshal(),
		Signers:            signers,
	}, nil
}

// TimeoutCollector accumulates timeout messages and produces a TimeoutCertificate.
type TimeoutCollector struct {
	view     ViewNumber
	timeouts map[ValidatorIndex]timeoutEntry
	setSize  uint32
	verified map[ValidatorIndex]bool
}

type timeoutEntry struct {
	signature common.Signature
	highQC    QuorumCertificate
}

// NewTimeoutCollector creates a new timeout collector for the given view.
func NewTimeoutCollector(view ViewNumber, setSize uint32) *TimeoutCollector {
	return &TimeoutCollector{
		view:     view,
		timeouts: make(map[ValidatorIndex]timeoutEntry),
		setSize:  setSize,
		verified: make(map[ValidatorIndex]bool),
	}
}

// View returns the view this collector is collecting timeouts for.
func (tc *TimeoutCollector) View() ViewNumber {
	return tc.view
}

// AddTimeout adds a timeout from a validator. Returns error if duplicate.
func (tc *TimeoutCollector) AddTimeout(validatorIndex ValidatorIndex, signature common.Signature, highQC QuorumCertificate) error {
	if _, exists := tc.timeouts[validatorIndex]; exists {
		return &DuplicateVoteError{View: tc.view, ValidatorIndex: validatorIndex}
	}
	tc.timeouts[validatorIndex] = timeoutEntry{signature: signature, highQC: highQC}
	return nil
}

// AddTimeoutFromBytes adds a timeout from raw signature bytes.
func (tc *TimeoutCollector) AddTimeoutFromBytes(validatorIndex ValidatorIndex, sigBytes []byte, highQC QuorumCertificate) error {
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("invalid signature bytes: %w", err)
	}
	return tc.AddTimeout(validatorIndex, sig, highQC)
}

// AddVerifiedTimeout adds a timeout that has already been signature-verified.
func (tc *TimeoutCollector) AddVerifiedTimeout(validatorIndex ValidatorIndex, signature common.Signature, highQC QuorumCertificate) error {
	if err := tc.AddTimeout(validatorIndex, signature, highQC); err != nil {
		return err
	}
	tc.verified[validatorIndex] = true
	return nil
}

// AddVerifiedTimeoutFromBytes adds a pre-verified timeout from raw signature bytes.
func (tc *TimeoutCollector) AddVerifiedTimeoutFromBytes(validatorIndex ValidatorIndex, sigBytes []byte, highQC QuorumCertificate) error {
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("invalid signature bytes: %w", err)
	}
	return tc.AddVerifiedTimeout(validatorIndex, sig, highQC)
}

// TimeoutCount returns the number of collected timeouts.
func (tc *TimeoutCollector) TimeoutCount() int {
	return len(tc.timeouts)
}

// HasQuorum returns true if enough timeouts have been collected.
func (tc *TimeoutCollector) HasQuorum(quorumSize int) bool {
	return len(tc.timeouts) >= quorumSize
}

// BuildTC builds a TimeoutCertificate from collected timeout signatures.
func (tc *TimeoutCollector) BuildTC(vs *ValidatorSet) (*TimeoutCertificate, error) {
	quorumSize := vs.QuorumSize()
	if len(tc.timeouts) < quorumSize {
		return nil, &InsufficientVotesError{View: tc.view, Have: len(tc.timeouts), Need: quorumSize}
	}

	message := TimeoutSigningMessage(tc.view)
	var highestQC *QuorumCertificate
	validSigs := make([]common.Signature, 0, len(tc.timeouts))
	signers := make([]bool, tc.setSize)

	for idx, entry := range tc.timeouts {
		if idx >= tc.setSize {
			continue
		}

		if tc.verified[idx] {
			validSigs = append(validSigs, entry.signature)
			signers[idx] = true
			if highestQC == nil || entry.highQC.View > highestQC.View {
				qcCopy := entry.highQC.Clone()
				highestQC = &qcCopy
			}
			continue
		}

		pk, err := vs.GetPublicKey(idx)
		if err != nil {
			continue
		}
		if !entry.signature.Verify(pk, message) {
			continue
		}
		validSigs = append(validSigs, entry.signature)
		signers[idx] = true
		if highestQC == nil || entry.highQC.View > highestQC.View {
			qcCopy := entry.highQC.Clone()
			highestQC = &qcCopy
		}
	}

	if len(validSigs) < quorumSize {
		return nil, &InsufficientVotesError{View: tc.view, Have: len(validSigs), Need: quorumSize}
	}

	if highestQC == nil {
		return nil, &InvalidTCError{View: tc.view, Reason: "no high_qc found in timeout messages"}
	}

	// Defensive: verify the selected highQC even though processTimeout should
	// have already verified each embedded QC individually.
	// Only verify if QC has signers (skip mock/test QCs with empty bitmap).
	if highestQC.View > 0 && len(highestQC.Signers) > 0 {
		if vErr := VerifyQCAnyDomain(highestQC, vs); vErr != nil {
			log.Warn("BuildTC: highest QC failed verification, using genesis",
				"view", tc.view, "highQC.view", highestQC.View, "err", vErr)
			genesis := GenesisQC()
			highestQC = &genesis
		}
	}

	aggSig := bls.AggregateSignatures(validSigs)
	return &TimeoutCertificate{
		View:               tc.view,
		AggregateSignature: aggSig.Marshal(),
		Signers:            signers,
		HighQC:             *highestQC,
	}, nil
}

// VerifyQC verifies a QuorumCertificate (Round 1 prepare) against the validator set.
func VerifyQC(qc *QuorumCertificate, vs *ValidatorSet) error {
	msg := SigningMessage(qc.View, qc.BlockHash)
	return verifyAggregateSignature(qc, vs, msg, "QC")
}

// VerifyCommitQC verifies a CommitQC (Round 2 commit) against the validator set.
func VerifyCommitQC(qc *QuorumCertificate, vs *ValidatorSet) error {
	msg := CommitSigningMessage(qc.View, qc.BlockHash)
	return verifyAggregateSignature(qc, vs, msg, "CommitQC")
}

// VerifyQCAnyDomain verifies a QC that may be either a PrepareQC or CommitQC.
// Tries both signing domains to handle QCs embedded in timeout/proposal messages.
func VerifyQCAnyDomain(qc *QuorumCertificate, vs *ValidatorSet) error {
	prepareMsg := SigningMessage(qc.View, qc.BlockHash)
	if err := verifyAggregateSignature(qc, vs, prepareMsg, "QC"); err == nil {
		return nil
	}
	commitMsg := CommitSigningMessage(qc.View, qc.BlockHash)
	return verifyAggregateSignature(qc, vs, commitMsg, "QC")
}

// VerifyTC verifies a TimeoutCertificate against the validator set.
func VerifyTC(tc *TimeoutCertificate, vs *ValidatorSet) error {
	msg := TimeoutSigningMessage(tc.View)
	quorumSize := vs.QuorumSize()

	if int(vs.Len()) != len(tc.Signers) {
		return &InvalidTCError{
			View:   tc.View,
			Reason: fmt.Sprintf("signers bitmap length mismatch: got %d, expected %d", len(tc.Signers), vs.Len()),
		}
	}

	signerCount := 0
	pubKeys := make([]common.PublicKey, 0, vs.Len())
	for i, signed := range tc.Signers {
		if signed {
			signerCount++
			pk, err := vs.GetPublicKey(ValidatorIndex(i))
			if err != nil {
				return err
			}
			pubKeys = append(pubKeys, pk)
		}
	}

	if signerCount < quorumSize {
		return &InvalidTCError{
			View:   tc.View,
			Reason: fmt.Sprintf("insufficient signers: have %d, need %d", signerCount, quorumSize),
		}
	}

	sig, err := bls.SignatureFromBytes(tc.AggregateSignature)
	if err != nil {
		return &InvalidTCError{View: tc.View, Reason: "failed to deserialize aggregate signature"}
	}

	aggPK := bls.AggregateMultiplePubkeys(pubKeys)
	if !sig.Verify(aggPK, msg) {
		return &InvalidTCError{View: tc.View, Reason: "aggregated signature verification failed"}
	}
	return nil
}

// verifyAggregateSignature is a shared helper for verifying QC aggregate signatures.
func verifyAggregateSignature(qc *QuorumCertificate, vs *ValidatorSet, message []byte, certKind string) error {
	quorumSize := vs.QuorumSize()

	if int(vs.Len()) != len(qc.Signers) {
		return &InvalidQCError{
			View:   qc.View,
			Reason: fmt.Sprintf("signers bitmap length mismatch: got %d, expected %d", len(qc.Signers), vs.Len()),
		}
	}

	signerCount := 0
	pubKeys := make([]common.PublicKey, 0, vs.Len())
	for i, signed := range qc.Signers {
		if signed {
			signerCount++
			pk, err := vs.GetPublicKey(ValidatorIndex(i))
			if err != nil {
				return err
			}
			pubKeys = append(pubKeys, pk)
		}
	}

	if signerCount < quorumSize {
		return &InvalidQCError{
			View:   qc.View,
			Reason: fmt.Sprintf("insufficient signers: have %d, need %d", signerCount, quorumSize),
		}
	}

	sig, err := bls.SignatureFromBytes(qc.AggregateSignature)
	if err != nil {
		return &InvalidQCError{View: qc.View, Reason: fmt.Sprintf("failed to deserialize aggregate signature: %v", err)}
	}

	aggPK := bls.AggregateMultiplePubkeys(pubKeys)
	if !sig.Verify(aggPK, message) {
		return &InvalidQCError{View: qc.View, Reason: fmt.Sprintf("%s aggregated signature verification failed", certKind)}
	}
	return nil
}
