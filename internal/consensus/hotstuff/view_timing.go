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

// durOK builds a PhaseDuration from a raw duration plus whether it was ever
// recorded -- the contentionStamps fields carry their own bool rather than a
// pointer, since they are written in place under e.mu, never reallocated.
func durOK(d time.Duration, ok bool) PhaseDuration {
	if !ok {
		return PhaseDuration{}
	}
	return PhaseDuration{D: d, OK: true}
}

// contentionStamps accumulates S14's per-view vote-path contention timing
// (diagnostic only, N42_CONTENTION_DIAG=1; docs/QS_BLOCK_TIME_BUDGET.md
// 6cb-6ce found ~450ms of an in-tenure cycle's 520ms consensus round-trip
// unattributed to any named wait). Every field here is engine state: read
// and written only from inside a call that ProcessEvent already made under
// e.mu (engine.go:596-598), so recording costs a few time.Now() calls and
// zero extra locking or allocation.
//
// The serialising point timed here is e.mu itself (a plain sync.Mutex,
// engine.go): ProcessEvent takes it for every inbound message AND for every
// block-imported/checked/rejected/ready event, and it is held for the whole
// handler call, not released and reacquired -- there is no separate
// event-loop dispatch to time on top of it.
//
//	t_arrive  stamped in processGossipMessage (service.go), the first line
//	          of the network handler, before snappy-decompress/RLP-decode.
//	          Threaded down as ConsensusEvent.ReceivedAt -> ProcessEvent ->
//	          processMessage -> dispatchMessage -> the process* handler.
//	t_locked  stamped at the top of processVote/processCommitVote/
//	          processProposal/processPrepareQC -- the first code that runs
//	          once e.mu is held for this message, so (t_locked - t_arrive)
//	          is decode time plus any queueing behind e.mu's current holder
//	          (another message, or a block-imported/checked notification).
//	t_done    stamped when that handler finishes its own work (not
//	          including a QC-formation call it triggers afterward -- see
//	          kthArrival below for that).
//
// Fields, leader side (round1/round2 -- this node's own Round-1/Round-2
// vote collection for the CURRENT view):
//
//	n                  votes counted toward this round so far
//	lockWaitSum/Max    sum/max of (t_locked - t_arrive) over those votes
//	workSum            sum of (t_done - t_locked) -- each vote's own
//	                   decode/buffer/maybe-batch-verify-flush cost, NOT
//	                   including tryFormPrepareQC/tryFormCommitQC's own QC-
//	                   build cost, which instead falls between kthArrival
//	                   and the round's QC-formed timestamp (see below)
//	kthArrival         t_arrive of the QUORUM-COMPLETING (k-th) vote, i.e.
//	                   the vote whose arrival first made
//	                   collectorCount+bufferedCount >= QuorumSize true.
//	                   "roundStart -> kthArrival" is derived at Phases()
//	                   time as KthArrivalOffset; "kthArrival -> QC-formed"
//	                   as QCMinusKth -- together they split a round's total
//	                   into "waiting for enough votes" vs "aggregating once
//	                   there were enough."
//
// Fields, follower side (this node's own handling of the CURRENT view's one
// Proposal and one PrepareQC message):
//
//	proposalLockWait/Work, prepareQCLockWait/Work   as above, per message
//	prepareQCArrival   t_arrive of the PrepareQC message; CommitVoteSent
//	                   (existing ViewTiming field) minus this is the full
//	                   local cost of producing a Round-2 vote once the
//	                   PrepareQC is in hand, whether sent immediately or
//	                   held and released later
//	commitVoteHeld     true if the two-phase gate held this view's commit
//	                   vote (processPrepareQC) rather than sending it right
//	                   away
//	commitVoteGate     set when a held vote is released
//	                   (castHeldCommitVoteIfAttested): "own-import" (this
//	                   block's own EventBlockImported satisfied the gate),
//	                   "parent-import" (a DIFFERENT import -- in practice
//	                   the parent's -- satisfied deferredAttested), or
//	                   "checked" (EventBlockChecked satisfied it). Empty
//	                   when the vote was never held.
type contentionStamps struct {
	round1, round2 roundContention

	proposalLockWait, proposalWork     time.Duration
	proposalLockWaitOK, proposalWorkOK bool

	prepareQCLockWait, prepareQCWork     time.Duration
	prepareQCLockWaitOK, prepareQCWorkOK bool
	prepareQCArrival                     time.Time

	commitVoteHeld bool
	commitVoteGate string
}

// roundContention is one round's (Round 1 or Round 2) leader-side vote
// contention accumulator. See contentionStamps for field meanings.
type roundContention struct {
	n                        int
	lockWaitSum, lockWaitMax time.Duration
	workSum                  time.Duration
	kthArrival               time.Time
	kthSet                   bool
}

// record adds one vote's timing to the round accumulator. quorumReached is
// whether THIS vote's arrival is what first made the collector's count (plus
// still-buffered, not-yet-verified votes) reach quorum size -- the caller
// computes that from state it already has (VoteCollector.VoteCount() plus
// len(prepareVoteBuf)/len(commitVoteBuf)), since roundContention has no
// engine access of its own.
func (r *roundContention) record(tArrive, tLocked, tDone time.Time, quorumReached bool) {
	if tArrive.IsZero() || tLocked.IsZero() {
		return
	}
	lw := tLocked.Sub(tArrive)
	if lw < 0 {
		lw = 0
	}
	r.n++
	r.lockWaitSum += lw
	if lw > r.lockWaitMax {
		r.lockWaitMax = lw
	}
	if !tDone.IsZero() {
		if wk := tDone.Sub(tLocked); wk > 0 {
			r.workSum += wk
		}
	}
	if !r.kthSet && quorumReached {
		r.kthArrival = tArrive
		r.kthSet = true
	}
}

// RoundContentionPhase is roundContention's derived, log/metrics-ready form.
type RoundContentionPhase struct {
	N                            int
	LockWaitSum, LockWaitMax     PhaseDuration
	WorkSum                      PhaseDuration
	KthArrivalOffset, QCMinusKth PhaseDuration
}

// phase derives a RoundContentionPhase. roundStart/qcFormed are the round's
// own start and QC-formed timestamps (ProposalSent/PrepareQCFormed for
// Round 1, PrepareQCFormed/CommitQCFormed for Round 2).
func (r roundContention) phase(roundStart, qcFormed *time.Time) RoundContentionPhase {
	p := RoundContentionPhase{N: r.n}
	if r.n == 0 {
		return p
	}
	p.LockWaitSum = PhaseDuration{D: r.lockWaitSum, OK: true}
	p.LockWaitMax = PhaseDuration{D: r.lockWaitMax, OK: true}
	p.WorkSum = PhaseDuration{D: r.workSum, OK: true}
	if r.kthSet {
		kth := r.kthArrival
		p.KthArrivalOffset = span(roundStart, &kth)
		p.QCMinusKth = span(&kth, qcFormed)
	}
	return p
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

	// S14 contention diagnostics (N42_CONTENTION_DIAG=1 only; see
	// contentionStamps for what each field means and where it is stamped).
	// All zero/OK=false/empty when the switch is off -- LogLine's output is
	// then byte-identical to before this field set existed.
	Round1Contention RoundContentionPhase // leader only
	Round2Contention RoundContentionPhase // leader only

	ProposalLockWait, ProposalWork   PhaseDuration // follower only
	PrepareQCLockWait, PrepareQCWork PhaseDuration // follower only
	PrepareQCToCommitVote            PhaseDuration // follower only: prepareQCArrival -> CommitVoteSent
	CommitVoteHeld                   bool          // follower only
	CommitVoteGate                   string        // follower only, set when CommitVoteHeld
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
		p.Round1Contention = t.Contention.round1.phase(t.ProposalSent, t.PrepareQCFormed)
		p.Round2Contention = t.Contention.round2.phase(t.PrepareQCFormed, t.CommitQCFormed)
	case RoleFollower:
		p.Delivery = span(&start, t.ProposalReceived)
		p.ExecWait = span(t.ProposalReceived, t.VoteSent)
		p.Round1 = span(t.VoteSent, t.CommitVoteSent)
		p.Round2 = span(t.CommitVoteSent, t.CommitQCFormed)
		c := t.Contention
		p.ProposalLockWait = durOK(c.proposalLockWait, c.proposalLockWaitOK)
		p.ProposalWork = durOK(c.proposalWork, c.proposalWorkOK)
		p.PrepareQCLockWait = durOK(c.prepareQCLockWait, c.prepareQCLockWaitOK)
		p.PrepareQCWork = durOK(c.prepareQCWork, c.prepareQCWorkOK)
		if !c.prepareQCArrival.IsZero() {
			arrival := c.prepareQCArrival
			p.PrepareQCToCommitVote = span(&arrival, t.CommitVoteSent)
		}
		p.CommitVoteHeld = c.commitVoteHeld
		p.CommitVoteGate = c.commitVoteGate
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

	// S14 (N42_CONTENTION_DIAG=1 only): silent (all N==0 / OK==false) when
	// the switch is off, so this appends nothing to the line above.
	appendRound := func(prefix string, rc RoundContentionPhase) {
		if rc.N == 0 {
			return
		}
		line += fmt.Sprintf(" %sn=%d %slw=%dms %slwMax=%dms %swk=%dms",
			prefix, rc.N, prefix, rc.LockWaitSum.Ms(), prefix, rc.LockWaitMax.Ms(), prefix, rc.WorkSum.Ms())
		if rc.KthArrivalOffset.OK {
			line += fmt.Sprintf(" %skth=%dms %sqk=%dms", prefix, rc.KthArrivalOffset.Ms(), prefix, rc.QCMinusKth.Ms())
		}
	}
	appendRound("r1", p.Round1Contention)
	appendRound("r2", p.Round2Contention)
	appendMs("propLw", p.ProposalLockWait)
	appendMs("propWk", p.ProposalWork)
	appendMs("pqcLw", p.PrepareQCLockWait)
	appendMs("pqcWk", p.PrepareQCWork)
	appendMs("pqc2cv", p.PrepareQCToCommitVote)
	if p.Role == RoleFollower && p.PrepareQCLockWait.OK {
		line += fmt.Sprintf(" cvHeld=%v", p.CommitVoteHeld)
		if p.CommitVoteGate != "" {
			line += fmt.Sprintf(" cvGate=%s", p.CommitVoteGate)
		}
	}
	return line
}
