// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Late vote equivocation (E15, AM34 item 6): a leader advances the view the
// moment its CommitQC forms, so an R1 or commit vote that arrives afterwards
// — from a member outside the quorum, or the second of a conflicting pair —
// lands in a closed view and used to be dropped by processMessage before any
// equivocation check. A member that double-signed then went unproven.
//
// For the last lateVoteWindow views this node led, the verified first vote of
// each voter (the view's R1 and commit trackers) is kept together with the
// validator set of that view, and late votes are kept as they arrive,
// unverified. A signature is checked only when two votes of one voter and
// round conflict, so an honest late vote costs one map lookup and insert.
// No QC is re-formed and nothing else is done with late votes.

package hotstuff

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// lateVoteWindow is how many views back a late vote is still checked
// (AM34 item 6 requires at least 3).
const lateVoteWindow = 8

// signedVote is one vote: the block hash and its signature bytes.
type signedVote struct {
	hash types.Hash
	sig  []byte
}

// lateVoteBook holds one round's votes of a closed view.
type lateVoteBook struct {
	// verified holds the first verified vote per voter (the view's tracker at
	// close); late holds the first late one, unverified.
	verified map[ValidatorIndex]signedVote
	late     map[ValidatorIndex]signedVote
	// reported marks voters whose equivocation was already emitted, so
	// further conflicting copies cost nothing.
	reported map[ValidatorIndex]bool
}

func newLateVoteBook(tracker map[ValidatorIndex]types.Hash, sigs map[ValidatorIndex][]byte) *lateVoteBook {
	verified := make(map[ValidatorIndex]signedVote, len(tracker))
	for voter, hash := range tracker {
		verified[voter] = signedVote{hash: hash, sig: sigs[voter]}
	}
	return &lateVoteBook{
		verified: verified,
		late:     make(map[ValidatorIndex]signedVote),
		reported: make(map[ValidatorIndex]bool),
	}
}

// closedLedView is a view this node led, kept for late votes.
type closedLedView struct {
	view    ViewNumber
	vs      *ValidatorSet
	prepare *lateVoteBook
	commit  *lateVoteBook
}

// closeLedView keeps the R1 and commit votes of the view being left when this
// node led it. Called by advanceToView before the trackers are reset and
// before any epoch boundary is crossed (vs is still the closing view's set).
func (e *ConsensusEngine) closeLedView() {
	view := e.roundState.CurrentView()
	vs := e.validatorSet()
	if vs == nil || !e.isLeader(e.myIndex, view, vs) {
		return
	}
	rec := &closedLedView{
		view:    view,
		vs:      vs,
		prepare: newLateVoteBook(e.equivocationTracker, e.equivocationSigs),
		commit:  newLateVoteBook(e.commitEquivocationTracker, e.commitEquivocationSigs),
	}
	kept := e.closedLedViews[:0]
	for _, c := range e.closedLedViews {
		if c.view != view && c.view+lateVoteWindow >= view {
			kept = append(kept, c)
		}
	}
	e.closedLedViews = append(kept, rec)
}

// checkLateVote checks an R1 vote for a view this node has left.
func (e *ConsensusEngine) checkLateVote(v *Vote) {
	e.checkLateSigned(false, v.View, v.Voter, v.BlockHash, v.Signature)
}

// checkLateCommitVote checks a commit vote for a view this node has left.
func (e *ConsensusEngine) checkLateCommitVote(cv *CommitVote) {
	e.checkLateSigned(true, cv.View, cv.Voter, cv.BlockHash, cv.Signature)
}

// checkLateSigned checks a late vote of one round against the votes kept for
// its view and emits evidence for a verified conflicting pair. A late vote is
// never acted on otherwise, so nothing is returned.
func (e *ConsensusEngine) checkLateSigned(commit bool, view ViewNumber, voter ValidatorIndex, hash types.Hash, sig []byte) {
	current := e.roundState.CurrentView()
	if view >= current || current-view > lateVoteWindow {
		return
	}
	var rec *closedLedView
	for _, c := range e.closedLedViews {
		if c.view == view {
			rec = c
			break
		}
	}
	if rec == nil {
		return
	}
	book, signing, kind := rec.prepare, e.voteSigningMessage, EvidenceKindVote
	if commit {
		book, signing, kind = rec.commit, e.commitSigningMessage, EvidenceKindCommitVote
	}
	if book.reported[voter] {
		return
	}
	pk, err := rec.vs.GetPublicKey(voter)
	if err != nil {
		return
	}

	first, ok := book.verified[voter]
	if ok {
		if first.hash == hash {
			return
		}
	} else {
		late, seen := book.late[voter]
		if !seen {
			book.late[voter] = signedVote{hash: hash, sig: append([]byte(nil), sig...)}
			return
		}
		if late.hash == hash {
			return
		}
		if !VerifyBLSSignature(late.sig, pk, signing(view, late.hash)) {
			// A forged first: the slot goes to this one.
			book.late[voter] = signedVote{hash: hash, sig: append([]byte(nil), sig...)}
			return
		}
		first = late
	}

	msg2 := signing(view, hash)
	if !VerifyBLSSignature(sig, pk, msg2) {
		return
	}
	book.reported[voter] = true
	log.Warn("late vote equivocation detected", "commit", commit,
		"view", view, "validator", voter, "hash1", first.hash, "hash2", hash)
	_ = e.emit(EngineOutput{
		Type:      OutputEquivocationDetected,
		View:      view,
		Validator: voter,
		Hash1:     first.hash,
		Hash2:     hash,

		EvidenceKind: kind,
		PublicKey:    pk.Marshal(),
		Msg1:         signing(view, first.hash),
		Msg2:         msg2,
		Sig1:         first.sig,
		Sig2:         append([]byte(nil), sig...),
	})
}
