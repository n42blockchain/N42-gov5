// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Observability for the per-view HotStuff timestamps collected in ViewTiming.
// Turns the raw timestamps into per-stage durations (Phases), publishes the
// last committed view's timing for out-of-engine readers (LastCommittedTiming),
// and exports it as Prometheus histograms plus one compact log line per commit.
//
// This file is purely diagnostic: nothing here influences voting, timeouts or
// block production.

package hotstuff

import (
	"fmt"
	"time"

	"github.com/n42blockchain/N42/log"
)

// ViewRole is the role this node played in a view, inferred from which
// ViewTiming fields were populated.
type ViewRole uint8

const (
	// RoleUnknown means the view produced neither a sent nor a received
	// proposal (e.g. a commit adopted from a Decide without ever seeing the
	// proposal).
	RoleUnknown ViewRole = iota
	// RoleLeader means this node proposed in the view.
	RoleLeader
	// RoleFollower means this node received a proposal from the leader.
	RoleFollower
)

func (r ViewRole) String() string {
	switch r {
	case RoleLeader:
		return "leader"
	case RoleFollower:
		return "follower"
	default:
		return "unknown"
	}
}

// PhaseDuration is a measured stage duration. OK is false when the stage could
// not be measured on this node (missing timestamp for its role, or a
// non-monotonic pair).
type PhaseDuration struct {
	D  time.Duration
	OK bool
}

// Ms returns the duration in whole milliseconds, or -1 when not measured.
func (p PhaseDuration) Ms() int64 {
	if !p.OK {
		return -1
	}
	return p.D.Milliseconds()
}

// ViewPhases is the derived per-stage breakdown of one view.
//
// Leader view:
//
//	Propose  ViewStart        → ProposalSent      (wait for the sealed block)
//	Round1   ProposalSent     → PrepareQCFormed   (Round 1 vote round-trip)
//	Round2   PrepareQCFormed  → CommitQCFormed    (Round 2 vote round-trip)
//
// Follower view:
//
//	Delivery ViewStart        → ProposalReceived  (proposal propagation)
//	ExecWait ProposalReceived → VoteSent          (local execution/import wait
//	                                               under import-gated voting)
//	Round1   VoteSent         → CommitVoteSent    (prepare vote → PrepareQC in)
//	Round2   CommitVoteSent   → CommitQCFormed    (commit vote → Decide in)
//
// Both roles:
//
//	Total    ViewStart        → CommitQCFormed
type ViewPhases struct {
	View ViewNumber
	Role ViewRole

	Propose  PhaseDuration // leader only
	Delivery PhaseDuration // follower only
	ExecWait PhaseDuration // follower only
	Round1   PhaseDuration
	Round2   PhaseDuration
	Total    PhaseDuration

	PrepareVoteCount uint32
	CommitVoteCount  uint32
}

// span measures later-earlier, reporting not-OK when either endpoint is missing
// or the pair is non-monotonic (wall-clock adjustment).
func span(earlier, later *time.Time) PhaseDuration {
	if earlier == nil || later == nil || earlier.IsZero() || later.IsZero() {
		return PhaseDuration{}
	}
	d := later.Sub(*earlier)
	if d < 0 {
		return PhaseDuration{}
	}
	return PhaseDuration{D: d, OK: true}
}

// Role reports which side of the protocol this node was on for the view.
func (t ViewTiming) Role() ViewRole {
	switch {
	case t.ProposalSent != nil:
		return RoleLeader
	case t.ProposalReceived != nil:
		return RoleFollower
	default:
		return RoleUnknown
	}
}

// Phases derives the per-stage durations from the raw timestamps.
func (t ViewTiming) Phases() ViewPhases {
	start := t.ViewStart
	p := ViewPhases{
		View:             t.View,
		Role:             t.Role(),
		Total:            span(&start, t.CommitQCFormed),
		PrepareVoteCount: t.PrepareVoteCount,
		CommitVoteCount:  t.CommitVoteCount,
	}

	switch p.Role {
	case RoleLeader:
		p.Propose = span(&start, t.ProposalSent)
		p.Round1 = span(t.ProposalSent, t.PrepareQCFormed)
		p.Round2 = span(t.PrepareQCFormed, t.CommitQCFormed)
	case RoleFollower:
		p.Delivery = span(&start, t.ProposalReceived)
		p.ExecWait = span(t.ProposalReceived, t.VoteSent)
		p.Round1 = span(t.VoteSent, t.CommitVoteSent)
		p.Round2 = span(t.CommitVoteSent, t.CommitQCFormed)
	}
	return p
}

// LastCommittedTiming returns a copy of the timing of the most recently
// committed view and whether one has been recorded yet. Safe for concurrent
// use from outside the engine: it takes only the dedicated timing lock, never
// the engine lock, so it cannot deadlock against or stall consensus.
func (e *ConsensusEngine) LastCommittedTiming() (ViewTiming, bool) {
	e.timingMu.RLock()
	defer e.timingMu.RUnlock()
	if e.lastCommittedTiming == nil {
		return ViewTiming{}, false
	}
	return *e.lastCommittedTiming, true
}

// LastCommittedPhases returns the derived per-stage breakdown of the most
// recently committed view.
func (e *ConsensusEngine) LastCommittedPhases() (ViewPhases, bool) {
	t, ok := e.LastCommittedTiming()
	if !ok {
		return ViewPhases{}, false
	}
	return t.Phases(), true
}

// publishCommittedTiming stores the committed view's timing for observers and
// exports it (metrics + log). Called from advanceToView under the engine lock;
// timingMu is a leaf lock so the nesting is safe in one direction only —
// never acquire e.mu while holding timingMu.
func (e *ConsensusEngine) publishCommittedTiming(t ViewTiming) {
	timing := t
	e.timingMu.Lock()
	e.lastCommittedTiming = &timing
	e.timingMu.Unlock()

	phases := timing.Phases()
	updateMetricsViewTiming(phases)
	log.Info(phases.LogLine())
}

// LogLine renders the breakdown as a single compact, grep-friendly line.
// Unmeasured stages are omitted rather than reported as zero.
//
//	hotstuff view timing: view=421 role=leader propose=812ms r1=1503ms r2=1610ms total=3925ms votes=5/5
//	hotstuff view timing: view=422 role=follower recv=118ms exec=764ms r1=1502ms r2=1548ms total=3932ms
func (p ViewPhases) LogLine() string {
	line := fmt.Sprintf("hotstuff view timing: view=%d role=%s", uint64(p.View), p.Role)
	appendMs := func(name string, d PhaseDuration) {
		if d.OK {
			line += fmt.Sprintf(" %s=%dms", name, d.Ms())
		}
	}
	appendMs("propose", p.Propose)
	appendMs("recv", p.Delivery)
	appendMs("exec", p.ExecWait)
	appendMs("r1", p.Round1)
	appendMs("r2", p.Round2)
	appendMs("total", p.Total)
	if p.PrepareVoteCount > 0 || p.CommitVoteCount > 0 {
		line += fmt.Sprintf(" votes=%d/%d", p.PrepareVoteCount, p.CommitVoteCount)
	}
	return line
}
