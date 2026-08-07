// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

var h2V4DomainPrefix = [7]byte{'N', '4', '2', 'H', '2', 'V', '4'}

type H2V4ChainIdentity struct {
	ChainID     uint64
	GenesisHash types.Hash
}

const (
	h2V4Proposal byte = 1
	h2V4Vote     byte = 2
	h2V4Commit   byte = 3
	h2V4Timeout  byte = 4
	h2V4NewView  byte = 5
)

func h2V4Base(identity H2V4ChainIdentity, phase byte, view ViewNumber) []byte {
	msg := make([]byte, 56)
	copy(msg[:7], h2V4DomainPrefix[:])
	msg[7] = phase
	binary.LittleEndian.PutUint64(msg[8:16], identity.ChainID)
	copy(msg[16:48], identity.GenesisHash[:])
	binary.LittleEndian.PutUint64(msg[48:56], view)
	return msg
}

func H2V4ProposalSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash, changesHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Proposal, view)
	msg = append(msg, blockHash[:]...)
	return append(msg, changesHash[:]...)
}

func H2V4VoteSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Vote, view)
	return append(msg, blockHash[:]...)
}

func H2V4CommitSigningMessage(identity H2V4ChainIdentity, view ViewNumber, blockHash, changesHash types.Hash) []byte {
	msg := h2V4Base(identity, h2V4Commit, view)
	msg = append(msg, blockHash[:]...)
	return append(msg, changesHash[:]...)
}

func H2V4TimeoutSigningMessage(identity H2V4ChainIdentity, view ViewNumber) []byte {
	return h2V4Base(identity, h2V4Timeout, view)
}

func H2V4NewViewSigningMessage(identity H2V4ChainIdentity, view ViewNumber) []byte {
	return h2V4Base(identity, h2V4NewView, view)
}

// EnableH2V4 switches this engine to the chain-bound cross-client signing
// profile. It must be called before the engine starts processing events.
func (e *ConsensusEngine) EnableH2V4(identity H2V4ChainIdentity) {
	e.h2V4Identity = &identity
}

func (e *ConsensusEngine) h2V4Enabled() bool { return e.h2V4Identity != nil }

// H2V4Enabled reports whether the explicitly configured static-validator
// cross-client profile is active.
func (e *ConsensusEngine) H2V4Enabled() bool { return e.h2V4Enabled() }

func (e *ConsensusEngine) proposalSigningMessage(view ViewNumber, blockHash types.Hash) []byte {
	if e.h2V4Identity != nil {
		return H2V4ProposalSigningMessage(*e.h2V4Identity, view, blockHash, types.Hash{})
	}
	return SigningMessage(view, blockHash)
}

func (e *ConsensusEngine) voteSigningMessage(view ViewNumber, blockHash types.Hash) []byte {
	if e.h2V4Identity != nil {
		return H2V4VoteSigningMessage(*e.h2V4Identity, view, blockHash)
	}
	return SigningMessage(view, blockHash)
}

func (e *ConsensusEngine) commitSigningMessage(view ViewNumber, blockHash types.Hash) []byte {
	if e.h2V4Identity != nil {
		return H2V4CommitSigningMessage(*e.h2V4Identity, view, blockHash, types.Hash{})
	}
	return CommitSigningMessage(view, blockHash)
}

func (e *ConsensusEngine) timeoutSigningMessage(view ViewNumber) []byte {
	if e.h2V4Identity != nil {
		return H2V4TimeoutSigningMessage(*e.h2V4Identity, view)
	}
	return TimeoutSigningMessage(view)
}

func (e *ConsensusEngine) newViewSigningMessage(view ViewNumber) []byte {
	if e.h2V4Identity != nil {
		return H2V4NewViewSigningMessage(*e.h2V4Identity, view)
	}
	return NewViewSigningMessage(view)
}

func (e *ConsensusEngine) verifyQC(qc *QuorumCertificate) error {
	return e.verifyQCWithSet(qc, e.validatorSet())
}

func (e *ConsensusEngine) verifyCommitQC(qc *QuorumCertificate) error {
	return e.verifyCommitQCWithSet(qc, e.validatorSet())
}

func (e *ConsensusEngine) verifyQCWithSet(qc *QuorumCertificate, vs *ValidatorSet) error {
	return verifyAggregateSignature(qc, vs, e.voteSigningMessage(qc.View, qc.BlockHash), "QC")
}

func (e *ConsensusEngine) verifyCommitQCWithSet(qc *QuorumCertificate, vs *ValidatorSet) error {
	return verifyAggregateSignature(qc, vs, e.commitSigningMessage(qc.View, qc.BlockHash), "CommitQC")
}

func (e *ConsensusEngine) verifyQCAnyDomain(qc *QuorumCertificate) error {
	return e.verifyQCAnyDomainWithSet(qc, e.validatorSet())
}

func (e *ConsensusEngine) verifyQCAnyDomainWithSet(qc *QuorumCertificate, vs *ValidatorSet) error {
	if err := e.verifyQCWithSet(qc, vs); err == nil {
		return nil
	}
	return e.verifyCommitQCWithSet(qc, vs)
}

func (e *ConsensusEngine) verifyTC(tc *TimeoutCertificate) error {
	return verifyTCAgainstMessage(tc, e.validatorSet(), e.timeoutSigningMessage(tc.View))
}

// verifyTCWithSet is verifyTC against an explicit validator set — the merge
// point of the v4 domain selection (this file) with the epoch-aware set
// resolution (resolveQCValidatorSet): certificates from an earlier epoch must
// verify against the set that actually signed them, under whichever signing
// domain this chain runs.
func (e *ConsensusEngine) verifyTCWithSet(tc *TimeoutCertificate, vs *ValidatorSet) error {
	return verifyTCAgainstMessage(tc, vs, e.timeoutSigningMessage(tc.View))
}

// VerifyCommitQCWithResolvedSet verifies a CommitQC against the validator set
// resolved for the QC's view (epoch-aware) under the chain's signing domain.
// Exported for the service layer's header-QC canonicalization path.
func (e *ConsensusEngine) VerifyCommitQCWithResolvedSet(qc *QuorumCertificate) error {
	return e.verifyCommitQCWithSet(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
}

// VerifyH2V4Decide verifies a chain-bound H2-v4 finality proof without
// advancing the local consensus engine. It is intended for non-voting
// observers and cross-client sync consumers.
func VerifyH2V4Decide(envelope *H2V4Envelope, vs *ValidatorSet) (*Decide, error) {
	if envelope == nil || envelope.Message == nil {
		return nil, errors.New("nil H2-v4 envelope")
	}
	if envelope.Message.Type != MsgDecide {
		return nil, errors.New("H2-v4 envelope is not a Decide message")
	}
	decide, ok := envelope.Message.Payload.(*Decide)
	if !ok || decide == nil {
		return nil, errors.New("invalid H2-v4 Decide payload")
	}
	if decide.View != decide.CommitQC.View || decide.BlockHash != decide.CommitQC.BlockHash {
		return nil, errors.New("H2-v4 Decide does not match its CommitQC")
	}
	message := H2V4CommitSigningMessage(
		envelope.Identity,
		decide.View,
		decide.BlockHash,
		envelope.ChangesHash,
	)
	if err := verifyAggregateSignature(&decide.CommitQC, vs, message, "H2V4CommitQC"); err != nil {
		return nil, err
	}
	return decide, nil
}
