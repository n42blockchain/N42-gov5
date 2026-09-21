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

	// send holds S17's sender-side stamps for this view, claimed from
	// ConsensusEngine.sendByView by publishCommittedTiming right before
	// Phases() runs. See perViewSendStamps.
	send perViewSendStamps

	// rx holds S17's receiver-side arrival stamps for this view. Unlike
	// send, these are written directly here (under e.mu, from inside
	// processProposal/processVote/processCommitVote/processPrepareQC),
	// since receipt is already funneled through the single-threaded
	// engine before those handlers run -- no extra lock needed.
	rx rxStamps
}

// sendMsgStamp is S17's sender-side timing for ONE message this node
// itself emitted in a view: t_emit (ConsensusEngine.emit) -> t_deq
// (processOutputs dequeues it) -> t_pub0/t_pub1 (bracketing the actual
// network call in handleBroadcast/handleSendToValidator, service.go).
// Recorded by recordSendStamp (engine.go), off the e.mu hot path.
type sendMsgStamp struct {
	emit2Deq time.Duration
	deq2Pub  time.Duration
	pubDur   time.Duration
	pubAtMs  int64  // t_pub1, unix ms -- for joining sender/receiver across nodes on the shared host clock
	path     string // "rotor ok" | "rotor failed -> gossip" | "gossip only"
	ok       bool
}

// perViewSendStamps is one view's sender-side stamps for the four hot
// message types. A node only ever sends a subset of these in a given
// view (a follower never sends PrepareQC; the leader never sends a
// prepare/commit vote through this path -- see tryFormPrepareQC's direct
// self-vote), so an unset field's ok is simply false.
type perViewSendStamps struct {
	proposal    sendMsgStamp
	prepareVote sendMsgStamp
	prepareQC   sendMsgStamp
	commitVote  sendMsgStamp
}

// kthRxStamps is S17's receiver-side timing for the QUORUM-COMPLETING
// (k-th) vote of one round: t_rx (earliest point the bytes were in this
// process) to t_arrive (existing S14 handler-entry stamp), the voter,
// which transport delivered it first, and the max rx2arr seen across
// every vote counted toward that round (a proxy for the round's slowest
// straggler, not just the deciding one).
type kthRxStamps struct {
	rx2Arr    time.Duration
	rxAtMs    int64
	voter     ValidatorIndex
	via       string // "rotor" | "gossip"
	maxRx2Arr time.Duration
	maxOK     bool
	ok        bool
}

// rxStamps is S17's receiver-side arrival timing for one view. pv/cv
// track the k-th prepare/commit vote (this node as leader); pqc tracks
// the one PrepareQC message (this node as follower). Duplicate arrivals
// (the same logical message via both Rotor and gossip -- "gossip is
// always sent" regardless of Rotor's own success, service.go) keep the
// FIRST arrival's stamps and only increment dupN; seen*Mask/seenPrepareQC
// are the dedup memory (bitmask by validator index for votes, since a
// validator set this small never needs more than a handful of bits).
type rxStamps struct {
	pqcRx2Arr time.Duration
	pqcRxAtMs int64
	pqcVia    string // "rotor" | "gossip" | "both" (a duplicate arrived via the other transport)
	pqcOK     bool

	pv, cv kthRxStamps

	dupN int

	seenPrepareQC       bool
	seenPrepareVoteMask uint64
	seenCommitVoteMask  uint64
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

// phase derives a MsgSendPhase from one recorded sendMsgStamp.
func (s sendMsgStamp) phase() MsgSendPhase {
	if !s.ok {
		return MsgSendPhase{}
	}
	return MsgSendPhase{
		Emit2Deq: PhaseDuration{D: s.emit2Deq, OK: true},
		Deq2Pub:  PhaseDuration{D: s.deq2Pub, OK: true},
		PubDur:   PhaseDuration{D: s.pubDur, OK: true},
		PubAtMs:  s.pubAtMs,
		Path:     s.path,
		OK:       true,
	}
}

// phase derives a MsgRxPhase from the follower's one-PrepareQC-per-view
// arrival stamps.
func (r rxStamps) pqcPhase() MsgRxPhase {
	if !r.pqcOK {
		return MsgRxPhase{}
	}
	return MsgRxPhase{
		Rx2Arr: PhaseDuration{D: r.pqcRx2Arr, OK: true},
		RxAtMs: r.pqcRxAtMs,
		Via:    r.pqcVia,
		OK:     true,
	}
}

// recordVoteRx records one vote's receiver-side rx2arr into its round's
// k-th/max accumulator, deduping per voter via mask so a duplicate arriving
// on the OTHER transport ("gossip is always sent" regardless of Rotor's
// own success, service.go) cannot inflate maxRx2Arr or be mistaken for a
// later, distinct k-th vote. quorumReached decides whether THIS vote is
// the (first) quorum-completing one, judged by the caller the same way
// roundContention.record's caller already does.
func recordVoteRx(rx *rxStamps, mask *uint64, kth *kthRxStamps, voter ValidatorIndex, mt msgTiming, quorumReached bool) {
	bit := uint64(1) << uint(voter%64)
	if *mask&bit != 0 {
		rx.dupN++
		return
	}
	*mask |= bit
	rx2arr := mt.arrive.Sub(mt.rx)
	if rx2arr < 0 {
		rx2arr = 0
	}
	if !kth.maxOK || rx2arr > kth.maxRx2Arr {
		kth.maxRx2Arr = rx2arr
		kth.maxOK = true
	}
	if !kth.ok && quorumReached {
		kth.rx2Arr = rx2arr
		kth.rxAtMs = mt.rx.UnixMilli()
		kth.voter = voter
		kth.via = mt.via
		kth.ok = true
	}
}

// phase derives a VoteRxPhase from one round's k-th-vote receiver stamps.
func (k kthRxStamps) phase() VoteRxPhase {
	if !k.ok {
		return VoteRxPhase{}
	}
	p := VoteRxPhase{
		KthRx2Arr: PhaseDuration{D: k.rx2Arr, OK: true},
		KthRxAtMs: k.rxAtMs,
		KthVoter:  k.voter,
		KthVia:    k.via,
		OK:        true,
	}
	if k.maxOK {
		p.KthMaxRx2Arr = PhaseDuration{D: k.maxRx2Arr, OK: true}
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

	// S17 diagnostics (N42_CONTENTION_DIAG=1 only; docs/QS_BLOCK_TIME_BUDGET.md
	// 6ck/6cl). Sender side: this node's own emit->publish timing for
	// whichever of the four hot messages it sent THIS view (a node sends a
	// subset depending on its role -- OK is false for the rest). Receiver
	// side: PrepareQC arrival (follower) and the k-th prepare/commit vote's
	// arrival (leader).
	SendProposal, SendPrepareVote, SendPrepareQC, SendCommitVote MsgSendPhase
	RxPrepareQC                                                  MsgRxPhase
	RxPrepareVote, RxCommitVote                                  VoteRxPhase
	DupN                                                         int
}

// MsgSendPhase is S17's derived sender-side timing for one message this
// node emitted in the view: Emit2Deq (t_emit->t_deq, engine.emit to
// processOutputs dequeuing it), Deq2Pub (t_deq->t_pub0, queueing behind
// this goroutine's own dispatch), PubDur (t_pub0->t_pub1, the actual
// network call(s)), PubAtMs (t_pub1, unix ms, for joining sender/receiver
// timestamps across nodes on the shared host clock -- 0 if OK is false),
// and Path ("rotor ok" | "rotor failed -> gossip" | "gossip only").
type MsgSendPhase struct {
	Emit2Deq, Deq2Pub, PubDur PhaseDuration
	PubAtMs                   int64
	Path                      string
	OK                        bool
}

// MsgRxPhase is S17's derived receiver-side timing for the one PrepareQC
// message a follower receives in a view: Rx2Arr (t_arrive-t_rx, the gap
// between the bytes reaching this process and the existing S14
// handler-entry stamp), RxAtMs (t_rx, unix ms), and Via ("rotor" |
// "gossip" | "both", if a duplicate arrived on the other transport).
type MsgRxPhase struct {
	Rx2Arr PhaseDuration
	RxAtMs int64
	Via    string
	OK     bool
}

// VoteRxPhase is S17's derived receiver-side timing for the
// QUORUM-COMPLETING (k-th) vote of a round, as seen by the leader:
// KthRx2Arr/KthRxAtMs/KthVoter/KthVia describe that one vote; KthMaxRx2Arr
// is the max Rx2Arr over every vote counted toward the round (the
// slowest straggler, not just the deciding one).
type VoteRxPhase struct {
	KthRx2Arr, KthMaxRx2Arr PhaseDuration
	KthRxAtMs               int64
	KthVoter                ValidatorIndex
	KthVia                  string
	OK                      bool
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

	// S17: sender/receiver stamps are role-independent -- a leader sends
	// Proposal+PrepareQC and receives votes; a follower sends votes and
	// receives PrepareQC. Each field's own OK flag is false when this node
	// did not play that part in this view.
	send := t.Contention.send
	p.SendProposal = send.proposal.phase()
	p.SendPrepareVote = send.prepareVote.phase()
	p.SendPrepareQC = send.prepareQC.phase()
	p.SendCommitVote = send.commitVote.phase()
	rx := t.Contention.rx
	p.RxPrepareQC = rx.pqcPhase()
	p.RxPrepareVote = rx.pv.phase()
	p.RxCommitVote = rx.cv.phase()
	p.DupN = rx.dupN

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
	// S17: claim this view's sender-side publish stamps (recorded off the
	// e.mu hot path by background publish goroutines -- see recordSendStamp)
	// before deriving Phases(), so "hotstuff view timing" carries them in
	// the same line as everything else. A slow publish that outlives the
	// view (network stall, degraded mesh) is simply not claimed here and
	// stays unattributed for this view -- see docs/QS_BLOCK_TIME_BUDGET.md
	// 6cl's method note.
	if contentionDiagEnabled {
		if send := e.takeSendStamps(t.View); send != nil {
			t.Contention.send = *send
		}
	}

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

	// S17 (N42_CONTENTION_DIAG=1 only; docs/QS_BLOCK_TIME_BUDGET.md 6ck/6cl):
	// sender-side emit->publish timing and receiver-side arrival timing for
	// the four hot messages. Silent (OK==false) when the switch is off or
	// this node did not play that part in the view.
	appendSend := func(prefix string, s MsgSendPhase, withPubAt bool) {
		if !s.OK {
			return
		}
		line += fmt.Sprintf(" %sEmit2Deq=%dms %sDeq2Pub=%dms %sPubDur=%dms %sPath=%q",
			prefix, s.Emit2Deq.Ms(), prefix, s.Deq2Pub.Ms(), prefix, s.PubDur.Ms(), prefix, s.Path)
		if withPubAt {
			line += fmt.Sprintf(" %sPubAt=%d", prefix, s.PubAtMs)
		}
	}
	appendSend("pr", p.SendProposal, false)
	appendSend("pv", p.SendPrepareVote, false)
	appendSend("pqc", p.SendPrepareQC, true)
	appendSend("cv", p.SendCommitVote, true)

	if p.RxPrepareQC.OK {
		line += fmt.Sprintf(" pqcRx2Arr=%dms pqcRxAt=%d pqcVia=%s",
			p.RxPrepareQC.Rx2Arr.Ms(), p.RxPrepareQC.RxAtMs, p.RxPrepareQC.Via)
	}
	appendKthRx := func(prefix string, v VoteRxPhase) {
		if !v.OK {
			return
		}
		line += fmt.Sprintf(" %sKthRx2Arr=%dms %sKthRxAt=%d %sKthVoter=%d %sKthVia=%s",
			prefix, v.KthRx2Arr.Ms(), prefix, v.KthRxAtMs, prefix, uint32(v.KthVoter), prefix, v.KthVia)
		if v.KthMaxRx2Arr.OK {
			line += fmt.Sprintf(" %sMaxRx2Arr=%dms", prefix, v.KthMaxRx2Arr.Ms())
		}
	}
	appendKthRx("pv", p.RxPrepareVote)
	appendKthRx("cv", p.RxCommitVote)
	if p.DupN > 0 {
		line += fmt.Sprintf(" dupN=%d", p.DupN)
	}
	return line
}
