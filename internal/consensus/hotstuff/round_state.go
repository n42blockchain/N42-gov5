// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// RoundState tracks the state of the current consensus round (view).
// A view progresses through phases WaitingForProposal to Voting to
// PreCommit to Committed; a timeout at any point transitions to
// TimedOut and triggers a view change. Holds the current view, phase
// and lockedQC (the safety lock preventing double-commits).

package hotstuff

import "github.com/n42blockchain/N42/common/types"

// RoundState tracks the state of the current consensus round (view).
//
// Each view progresses through phases:
//
//	WaitingForProposal → Voting → PreCommit → Committed
//
// A timeout at any point transitions to TimedOut, triggering view change.
type RoundState struct {
	currentView         ViewNumber
	phase               Phase
	lockedQC            QuorumCertificate   // highest QC (safety lock)
	lastCommittedQC     QuorumCertificate   // most recently committed
	highestTC           *TimeoutCertificate // highest known TC (SyncInfo piggyback)
	consecutiveTimeouts uint32

	// Double-vote prevention: track which view we've already voted in.
	votedInView       ViewNumber // Round 1 (Prepare) vote sent for this view
	commitVotedInView ViewNumber // Round 2 (Commit) vote sent for this view
	votedHash         types.Hash // block hash voted for in votedInView
}

// NewRoundState creates a new round state starting at view 1.
func NewRoundState() *RoundState {
	genesis := GenesisQC()
	return &RoundState{
		currentView:     1,
		phase:           PhaseWaitingForProposal,
		lockedQC:        genesis,
		lastCommittedQC: genesis,
	}
}

// FromSnapshot restores state from a persisted snapshot (crash recovery).
func RoundStateFromSnapshot(view ViewNumber, lockedQC, lastCommittedQC QuorumCertificate, consecutiveTimeouts uint32) *RoundState {
	return RoundStateFromSnapshotWithPhase(view, PhaseWaitingForProposal, lockedQC, lastCommittedQC, consecutiveTimeouts)
}

func RoundStateFromSnapshotWithPhase(view ViewNumber, phase Phase, lockedQC, lastCommittedQC QuorumCertificate, consecutiveTimeouts uint32) *RoundState {
	return &RoundState{
		currentView:         view,
		phase:               phase,
		lockedQC:            lockedQC,
		lastCommittedQC:     lastCommittedQC,
		consecutiveTimeouts: consecutiveTimeouts,
	}
}

// CurrentView returns the current view number.
func (rs *RoundState) CurrentView() ViewNumber {
	return rs.currentView
}

// Phase returns the current phase.
func (rs *RoundState) Phase() Phase {
	return rs.phase
}

// LockedQC returns the safety lock QC.
func (rs *RoundState) LockedQC() *QuorumCertificate {
	return &rs.lockedQC
}

// LastCommittedQC returns the last committed QC.
func (rs *RoundState) LastCommittedQC() *QuorumCertificate {
	return &rs.lastCommittedQC
}

// ConsecutiveTimeouts returns the timeout counter.
func (rs *RoundState) ConsecutiveTimeouts() uint32 {
	return rs.consecutiveTimeouts
}

// EnterVoting transitions to the Voting phase.
func (rs *RoundState) EnterVoting() {
	rs.phase = PhaseVoting
}

// EnterPreCommit transitions to the PreCommit phase.
func (rs *RoundState) EnterPreCommit() {
	rs.phase = PhasePreCommit
}

// Commit marks the current view as committed and updates QC state.
func (rs *RoundState) Commit(commitQC QuorumCertificate) {
	rs.phase = PhaseCommitted
	if commitQC.View > rs.lockedQC.View {
		rs.lockedQC = commitQC.Clone()
	}
	if commitQC.View >= rs.lastCommittedQC.View {
		rs.lastCommittedQC = commitQC.Clone()
	}
	rs.consecutiveTimeouts = 0
}

// AdvanceView advances to a new view, resetting the phase.
// Enforces monotonicity: the view can only move forward.
func (rs *RoundState) AdvanceView(newView ViewNumber) {
	if newView <= rs.currentView {
		return
	}
	rs.currentView = newView
	rs.phase = PhaseWaitingForProposal
}

// HasVotedInView returns true if a Round 1 vote was already sent for this view.
func (rs *RoundState) HasVotedInView(view ViewNumber) bool {
	return rs.votedInView == view && view > 0
}

// RecordVote records that a Round 1 vote was sent for this view.
func (rs *RoundState) RecordVote(view ViewNumber, hash types.Hash) {
	rs.votedInView = view
	rs.votedHash = hash
}

// VotedHashInView returns the block hash this node voted for in view, if it
// voted — used to RE-SEND the vote on a timeout re-broadcast (votes are
// single-shot gossip; on a lossy mesh the leader may have missed the vote even
// though the node cast it, and without a re-send the round can only time out).
func (rs *RoundState) VotedHashInView(view ViewNumber) (types.Hash, bool) {
	if rs.votedInView == view && view > 0 {
		return rs.votedHash, true
	}
	return types.Hash{}, false
}

// HasCommitVotedInView returns true if a Round 2 commit vote was already sent.
func (rs *RoundState) HasCommitVotedInView(view ViewNumber) bool {
	return rs.commitVotedInView == view && view > 0
}

// RecordCommitVote records that a Round 2 commit vote was sent for this view.
func (rs *RoundState) RecordCommitVote(view ViewNumber) {
	rs.commitVotedInView = view
}

// Timeout transitions to timed-out phase and increments the backoff counter.
func (rs *RoundState) Timeout() {
	rs.phase = PhaseTimedOut
	rs.consecutiveTimeouts++
	if rs.consecutiveTimeouts == 0 { // overflow protection
		rs.consecutiveTimeouts--
	}
}

// ResetConsecutiveTimeouts resets the timeout counter.
func (rs *RoundState) ResetConsecutiveTimeouts() {
	rs.consecutiveTimeouts = 0
}

// UpdateLockedQC updates the locked QC if the given QC has a higher view.
func (rs *RoundState) UpdateLockedQC(qc *QuorumCertificate) {
	if qc.View > rs.lockedQC.View {
		rs.lockedQC = qc.Clone()
	}
}

// HighestTC returns the highest TC seen so far (nil if none). The returned
// pointer is the stored value; callers must not mutate it. It is piggybacked
// (SyncInfo-style) on votes/timeouts so a lagging validator can jump views.
func (rs *RoundState) HighestTC() *TimeoutCertificate {
	return rs.highestTC
}

// UpdateHighestTC records tc as the highest known TC if it advances past the
// current one (by view).
func (rs *RoundState) UpdateHighestTC(tc *TimeoutCertificate) {
	if tc == nil {
		return
	}
	if rs.highestTC == nil || tc.View > rs.highestTC.View {
		clone := tc.Clone()
		rs.highestTC = &clone
	}
}

// IsSafeToVote implements the HotStuff-2 safety rule: a proposal is safe to vote on
// if its justify_qc extends the locked QC (justify_qc.view >= locked_qc.view).
func (rs *RoundState) IsSafeToVote(justifyQC *QuorumCertificate) bool {
	return justifyQC.View >= rs.lockedQC.View
}
