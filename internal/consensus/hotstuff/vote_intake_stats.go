// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"github.com/n42blockchain/N42/log"
)

// voteIntakeStats counts what the leader did with the Round 1 votes of one
// view, so a field run can tell "votes never arrived" from "votes arrived
// and were dropped" without debug logging. Logged once per view, when the
// view is left, and only on a node that received at least one vote.
type voteIntakeStats struct {
	view         ViewNumber
	received     int
	viewMismatch int
	notLeader    int
	badVoter     int
	hashMismatch int
	noCollector  int
	buffered     int
}

func (e *ConsensusEngine) voteIntake(view ViewNumber) *voteIntakeStats {
	if e.voteStats.view != view {
		e.logVoteIntake()
		e.voteStats = voteIntakeStats{view: view}
	}
	return &e.voteStats
}

// logVoteIntake reports the current view's counters, if any, and clears them.
func (e *ConsensusEngine) logVoteIntake() {
	s := e.voteStats
	if s.received == 0 {
		return
	}
	collected := 0
	if e.voteCollector != nil && e.voteCollector.view == s.view {
		collected = e.voteCollector.VoteCount()
	}
	log.Info("hotstuff: vote intake", "view", s.view, "received", s.received,
		"buffered", s.buffered, "collected", collected,
		"viewMismatch", s.viewMismatch, "notLeader", s.notLeader, "badVoter", s.badVoter,
		"hashMismatch", s.hashMismatch, "noCollector", s.noCollector)
	e.voteStats = voteIntakeStats{}
}
