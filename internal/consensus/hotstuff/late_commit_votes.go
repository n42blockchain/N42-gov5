// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Late commit-vote equivocation (E15): a leader advances the view the moment
// its CommitQC forms, so a commit vote that arrives afterwards — from a member
// outside the quorum, or the second of a conflicting pair — lands in a closed
// view and used to be dropped by processMessage before any equivocation check.
// A member that double-signed commit votes then went unproven.
//
// For the last lateCommitWindow views this node led, the verified first commit
// vote of each voter (the view's commit tracker) is kept together with the
// validator set of that view, and late commit votes are kept as they arrive,
// unverified. A signature is checked only when two commit votes of one voter
// conflict, so an honest late commit vote costs one map lookup and insert.
// No QC is re-formed and nothing else is done with late votes.

package hotstuff

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// lateCommitWindow is how many views back a late commit vote is still checked.
const lateCommitWindow = 8

// signedCommit is one commit vote: the block hash and its signature bytes.
type signedCommit struct {
	hash types.Hash
	sig  []byte
}

// closedCommitView is a view this node led, kept for late commit votes.
type closedCommitView struct {
	view ViewNumber
	vs   *ValidatorSet
	// verified holds the first verified commit vote per voter (the view's
	// commit tracker at close); late holds the first late one, unverified.
	verified map[ValidatorIndex]signedCommit
	late     map[ValidatorIndex]signedCommit
	// reported marks voters whose equivocation in this view was already
	// emitted, so further conflicting copies cost nothing.
	reported map[ValidatorIndex]bool
}

// closeCommitView keeps the commit votes of the view being left when this
// node led it. Called by advanceToView before the trackers are reset and
// before any epoch boundary is crossed (vs is still the closing view's set).
func (e *ConsensusEngine) closeCommitView() {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()
	if vs == nil || !e.isLeader(e.myIndex, view, vs) {
		return
	}
	verified := make(map[ValidatorIndex]signedCommit, len(e.commitEquivocationTracker))
	for voter, hash := range e.commitEquivocationTracker {
		verified[voter] = signedCommit{hash: hash, sig: e.commitEquivocationSigs[voter]}
	}
	rec := &closedCommitView{
		view:     view,
		vs:       vs,
		verified: verified,
		late:     make(map[ValidatorIndex]signedCommit),
		reported: make(map[ValidatorIndex]bool),
	}
	kept := e.closedCommitViews[:0]
	for _, c := range e.closedCommitViews {
		if c.view != view && c.view+lateCommitWindow >= view {
			kept = append(kept, c)
		}
	}
	e.closedCommitViews = append(kept, rec)
}

// checkLateCommitVote checks a commit vote for a view this node has left
// against the commit votes kept for that view and emits evidence for a
// verified conflicting pair. It never returns an error: a late vote is never
// acted on otherwise.
func (e *ConsensusEngine) checkLateCommitVote(cv *CommitVote) {
	current := e.roundState.CurrentView()
	if cv.View >= current || current-cv.View > lateCommitWindow {
		return
	}
	var rec *closedCommitView
	for _, c := range e.closedCommitViews {
		if c.view == cv.View {
			rec = c
			break
		}
	}
	if rec == nil || rec.reported[cv.Voter] {
		return
	}
	pk, err := rec.vs.GetPublicKey(cv.Voter)
	if err != nil {
		return
	}

	first, ok := rec.verified[cv.Voter]
	if ok {
		if first.hash == cv.BlockHash {
			return
		}
	} else {
		late, seen := rec.late[cv.Voter]
		if !seen {
			rec.late[cv.Voter] = signedCommit{hash: cv.BlockHash, sig: append([]byte(nil), cv.Signature...)}
			return
		}
		if late.hash == cv.BlockHash {
			return
		}
		if !VerifyBLSSignature(late.sig, pk, e.commitSigningMessage(cv.View, late.hash)) {
			// A forged first: the slot goes to this one.
			rec.late[cv.Voter] = signedCommit{hash: cv.BlockHash, sig: append([]byte(nil), cv.Signature...)}
			return
		}
		first = late
	}

	msg2 := e.commitSigningMessage(cv.View, cv.BlockHash)
	if !VerifyBLSSignature(cv.Signature, pk, msg2) {
		return
	}
	rec.reported[cv.Voter] = true
	log.Warn("late commit-vote equivocation detected",
		"view", cv.View, "validator", cv.Voter, "hash1", first.hash, "hash2", cv.BlockHash)
	_ = e.emit(EngineOutput{
		Type:      OutputEquivocationDetected,
		View:      cv.View,
		Validator: cv.Voter,
		Hash1:     first.hash,
		Hash2:     cv.BlockHash,

		EvidenceKind: EvidenceKindCommitVote,
		PublicKey:    pk.Marshal(),
		Msg1:         e.commitSigningMessage(cv.View, first.hash),
		Msg2:         msg2,
		Sig1:         first.sig,
		Sig2:         append([]byte(nil), cv.Signature...),
	})
}
