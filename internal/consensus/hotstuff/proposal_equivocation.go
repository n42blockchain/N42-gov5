// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Proposal equivocation detection (profile 1, M3d): a leader that signs two
// proposals with different block hashes for the same view is caught on
// receipt in either order, and the evidence carries both signed proposal
// messages exactly as signed (kind "proposal", RPC n42_equivocations).

package hotstuff

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/log"
)

// proposalEquivocationWindow bounds how many views keep their first verified
// proposal for equivocation detection.
const proposalEquivocationWindow = 256

// proposalRecord is the first verified proposal seen for a view.
type proposalRecord struct {
	proposer  ValidatorIndex
	blockHash types.Hash
	pk        common.PublicKey
	signed    []byte
	signature []byte
}

// observeProposal checks a proposal whose signature has ALREADY been verified
// against pk over signed. The first proposal for a view is recorded; a later
// one with a different block hash is equivocation: evidence is emitted and
// true is returned so the caller refuses it (no vote). An identical repeat
// returns false and is not evidence.
func (e *ConsensusEngine) observeProposal(p *Proposal, pk common.PublicKey, signed []byte) bool {
	if e.proposalSeen == nil {
		e.proposalSeen = make(map[ViewNumber]*proposalRecord)
	}
	rec, ok := e.proposalSeen[p.View]
	if !ok {
		e.proposalSeen[p.View] = &proposalRecord{
			proposer:  p.Proposer,
			blockHash: p.BlockHash,
			pk:        pk,
			signed:    append([]byte(nil), signed...),
			signature: append([]byte(nil), p.Signature...),
		}
		e.proposalSeenOrder = append(e.proposalSeenOrder, p.View)
		if len(e.proposalSeenOrder) > proposalEquivocationWindow {
			delete(e.proposalSeen, e.proposalSeenOrder[0])
			e.proposalSeenOrder = append(e.proposalSeenOrder[:0:0], e.proposalSeenOrder[1:]...)
		}
		return false
	}
	if rec.proposer != p.Proposer || rec.blockHash == p.BlockHash {
		return false
	}
	log.Warn("equivocation detected: leader signed two different proposals",
		"view", p.View, "leader", p.Proposer, "hash1", rec.blockHash, "hash2", p.BlockHash)
	_ = e.emit(EngineOutput{
		Type:      OutputEquivocationDetected,
		View:      p.View,
		Validator: p.Proposer,
		Hash1:     rec.blockHash,
		Hash2:     p.BlockHash,

		EvidenceKind: EvidenceKindProposal,
		PublicKey:    pk.Marshal(),
		Msg1:         rec.signed,
		Msg2:         append([]byte(nil), signed...),
		Sig1:         rec.signature,
		Sig2:         append([]byte(nil), p.Signature...),
	})
	return true
}

// checkStaleProposal handles a proposal for a view the node has already left:
// it is never acted on, but if it conflicts with the recorded first proposal
// of that view and its signature verifies under the same leader's key it is
// recorded as equivocation.
func (e *ConsensusEngine) checkStaleProposal(p *Proposal) {
	if p == nil || e.proposalSeen == nil {
		return
	}
	rec, ok := e.proposalSeen[p.View]
	if !ok || rec.proposer != p.Proposer || rec.blockHash == p.BlockHash {
		return
	}
	signed := e.proposalSigningMessage(p.View, p.BlockHash)
	if !VerifyBLSSignature(p.Signature, rec.pk, signed) {
		return
	}
	e.observeProposal(p, rec.pk, signed)
}
