// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// HotStuff-2 ConsensusEngine core state and protocol constants.
// Defines FutureViewWindow, the maximum number of views ahead a
// future message may be buffered. Owns the in-memory engine state:
// validator set, current view, pacemaker reference, local BLS key,
// vote collector and message buffers protected by a sync.Mutex.

package hotstuff

import (
	"os"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/log"
)

// contentionDiagEnabled gates S14's vote-path contention stamps (see
// contentionStamps in view_timing.go) and the runtime mutex/block profiling
// enabled alongside it in cmd/n42/app.go. Read once at start-up, same pattern
// as internal/miner's N42_BUILD_STALL_DIAG (which keeps working independently
// of this switch). Off by default: no time.Now() calls, no extra fields
// touched, no change to "hotstuff view timing"'s existing output.
// docs/QS_BLOCK_TIME_BUDGET.md 6cb-6ce found ~450ms of an in-tenure cycle's
// 520ms consensus round-trip unattributed to any named wait; this measures it.
var contentionDiagEnabled = os.Getenv("N42_CONTENTION_DIAG") == "1"

// Protocol constants.
const (
	// FutureViewWindow is the maximum number of views ahead a future message can be buffered.
	FutureViewWindow = 50

	// MaxFutureMessages is the maximum number of buffered future-view messages.
	MaxFutureMessages = 256

	// MaxImportedBlocks limits the imported block hash cache size.
	MaxImportedBlocks = 64

	// SyncGapThreshold triggers a SyncRequired output when the gap exceeds this.
	SyncGapThreshold = 3

	// MaxRecoveredConsecutiveTimeouts caps recovered consecutive timeouts.
	MaxRecoveredConsecutiveTimeouts = 128
)

// EngineOutput represents actions the consensus engine requests from the outer node.
type EngineOutput struct {
	Type    EngineOutputType
	Message *ConsensusMsg      // for Broadcast/SendToValidator
	Target  ValidatorIndex     // for SendToValidator
	View    ViewNumber         // for ViewChanged/SyncRequired/BlockCommitted
	Hash    types.Hash         // for ExecuteBlock/BlockCommitted
	QC      *QuorumCertificate // for BlockCommitted

	// SyncRequired fields
	LocalView  ViewNumber
	TargetView ViewNumber

	// Equivocation fields
	Validator ValidatorIndex
	Hash1     types.Hash
	Hash2     types.Hash

	// Epoch transition fields
	NewEpoch       uint64
	ValidatorCount uint32
	Removed        bool // true if this node was removed from the validator set

	// EmittedAt is S17's diagnostic sender-side stamp (t_emit): set inside
	// emit() itself, so every output carries it with no change at any call
	// site. Zero unless N42_CONTENTION_DIAG=1. See
	// docs/QS_BLOCK_TIME_BUDGET.md 6ck ("the stamps stop before the actual
	// publish") and ConsensusEngine.recordSendStamp.
	EmittedAt time.Time
}

// EngineOutputType identifies the kind of output action.
type EngineOutputType uint8

const (
	OutputBroadcast            EngineOutputType = 1
	OutputSendToValidator      EngineOutputType = 2
	OutputExecuteBlock         EngineOutputType = 3
	OutputBlockCommitted       EngineOutputType = 4
	OutputViewChanged          EngineOutputType = 5
	OutputSyncRequired         EngineOutputType = 6
	OutputEquivocationDetected EngineOutputType = 7
	OutputEpochTransition      EngineOutputType = 8
	OutputEpochStaged          EngineOutputType = 9 // staged validator set needs persistence
	// OutputSpeculativeBuild advises the block producer that this node just
	// voted for block Hash and — by round-robin — leads view View next, so a
	// proposal extending Hash can be built NOW, during the current view's
	// vote rounds. Purely advisory: acting on it, dropping it, or guessing
	// wrong never affects safety (the parked block is proposed only if the
	// real trigger confirms the same parent).
	OutputSpeculativeBuild EngineOutputType = 10
)

// ConsensusEngine is the HotStuff-2 consensus state machine.
//
// An event-driven engine that processes consensus messages and produces output actions.
// The outer node drives it via ProcessEvent() and OnTimeout().
//
// Protocol Flow (optimistic 2-round path):
//  1. Leader broadcasts Proposal{view, block_hash, justify_qc}
//  2. Validators verify and send Vote to leader
//  3. Leader forms PrepareQC, broadcasts PrepareQCMsg
//  4. Validators send CommitVote to leader
//  5. Leader forms CommitQC, broadcasts Decide, advances view
type ConsensusEngine struct {
	mu sync.Mutex

	myIndex   ValidatorIndex
	myAddr    types.Address // own validator address; lets an observer/removed node (myIndex==NonMemberIndex) locate itself in a new set at an epoch boundary
	secretKey common.SecretKey

	// h2V4Identity is set only for an explicitly configured cross-client
	// network. Nil preserves the deployed legacy signing domains exactly.
	h2V4Identity *H2V4ChainIdentity

	epochManager *EpochManager
	reconfigMgr  *ReconfigurationManager
	roundState   *RoundState
	pacemaker    *Pacemaker

	voteCollector    *VoteCollector
	commitCollector  *VoteCollector
	timeoutCollector *TimeoutCollector

	prepareQC         *QuorumCertificate
	previousPrepareQC *QuorumCertificate

	// Output channel for actions requested from the outer node.
	outputCh chan<- EngineOutput

	// Block tracking
	importedBlocks  map[types.Hash]bool
	importedParents map[types.Hash]types.Hash // imported blockHash → its parent hash (extends-check at vote time)
	importedFIFO    []types.Hash              // insertion order for bounded eviction; blocks stay known-imported across view changes
	// checkedBlocks: under deferred execution, blocks the service verified
	// without executing them (the header carries this node's result of the
	// parent; the transactions are includable). Such a block is voted for
	// once its parent is imported. Bounded like importedBlocks.
	checkedBlocks map[types.Hash]bool
	checkedFIFO   []types.Hash
	// checkedReference (S55, depth-2 deferred execution, docs/QS_BLOCK_TIME_BUDGET.md
	// 6f7): blockHash -> the ancestor deferredAttested must see imported,
	// when it differs from importedParents[blockHash] (the grandparent,
	// once N42_DEFERRED_EXECUTION_DEPTH2_TIME is active for blockHash's own
	// header time). Populated only by EventBlockChecked's own ReferenceHash
	// field, set by the caller that resolved it (CheckDeferredBlock already
	// computed the correct ancestor to validate the header against; this
	// just carries that same answer to the vote gate). Deliberately
	// SEPARATE from importedParents: extendsJustify reads importedParents
	// for the LITERAL parent-child chain relationship and must never see a
	// grandparent substituted in.
	checkedReference map[types.Hash]types.Hash
	// headerKnownFIFO (S31): bounded eviction for importedParents entries
	// populated ONLY by EventBlockHeaderKnown (the block has neither been
	// checked nor imported yet) -- see onBlockHeaderKnown.
	headerKnownFIFO  []types.Hash
	pendingTxRoots   map[types.Hash]types.Hash // blockHash → expected TxRootHash (DA verification)
	pendingProposals map[ViewNumber]types.Hash // view → proposed blockHash awaiting local import before the prepare vote (import-gated voting)
	// pendingJustifyBlocks records, per view, the proposal's JustifyQC.BlockHash
	// so the import-gated vote can enforce the HotStuff extends rule (the
	// proposed block's parent MUST be the justify block). Without it a proposal
	// can carry a high-view JustifyQC while extending a DIFFERENT branch, and
	// voters certify a chain conflicting with the justified one (observed live:
	// a dead same-height sibling re-proposed at a committed height).
	pendingJustifyBlocks      map[ViewNumber]types.Hash
	equivocationTracker       map[ValidatorIndex]types.Hash
	commitEquivocationTracker map[ValidatorIndex]types.Hash

	// twoPhaseVote (chainspec hotstuff.twoPhaseVoteGate) moves the execution
	// guarantee from Round 1 to Round 2 — the order-then-execute shape every
	// production HotStuff descendant uses. Round 1 votes on static validation
	// (leader signature, JustifyQC, DA commitment), decoupling view progress
	// from execution latency; the CommitVote is what waits for the local
	// import, so a CommitQC still proves 2f+1 validators executed the block
	// before it can commit. Off (default) = classic import-gated Round 1.
	twoPhaseVote    bool
	pendingCommitQC *PrepareQCMsg // two-phase R2 gate: PrepareQC held until the block imports

	// Batch BLS verification buffers (votes await batch pairing before
	// being added to the collector). Reset on every view change.
	prepareVoteBuf []pendingVote
	commitVoteBuf  []pendingVote

	// Future message buffer
	futureMsgBuffer []futureMsg
	// Latest verified future timeout reported by each validator. A weak
	// synchronizer may use f+1 distinct reports to recover from legitimately
	// scattered persisted views without trusting a single sender.
	futureTimeouts map[ValidatorIndex]*TimeoutMessage

	// Timing. viewTiming is engine state (guarded by e.mu); lastCommittedTiming
	// is published for out-of-engine observers and guarded by its own leaf lock
	// so a reader never contends with — or deadlocks against — the consensus
	// hot path. timingMu is only ever taken while nothing else is acquired.
	viewTiming          ViewTiming
	timingMu            sync.RWMutex
	lastCommittedTiming *ViewTiming

	// sendByView holds S17's sender-side publish stamps (t_emit -> t_deq ->
	// t_pub0/t_pub1), keyed by the view the message belongs to. Written from
	// handleBroadcast/handleSendToValidator's background goroutines --
	// AFTER the output has already left the engine via emit(), so these
	// goroutines never hold e.mu. sendMu is therefore a second leaf lock,
	// exactly like timingMu: never held while e.mu is held, e.mu never
	// acquired while it is held. publishCommittedTiming claims (pops) a
	// view's entry when that view commits; recordSendStamp bounds the map
	// so a view whose commit vote/broadcast never gets claimed (a timed-out
	// view) cannot leak forever.
	sendMu     sync.Mutex
	sendByView map[ViewNumber]*perViewSendStamps

	// Set when this validator is removed at an epoch boundary.
	removed bool

	// voteJournal durably records a vote commitment before the vote is released
	// to the network. Nil disables journalling (unit tests, embedded harnesses)
	// and restores the pre-journal behaviour.
	voteJournal VoteJournal

	// S19 (docs/QS_BLOCK_TIME_BUDGET.md 6co, write_latch.go): selfProposalHash
	// is the block THIS node most recently proposed as leader, tracked so
	// advanceToView can release a still-waiting write latch
	// (N42_LEADER_WRITE_AFTER_JOURNAL) if the view is abandoned (a timeout)
	// before journalCommitVote ever ran for it. Guarded by e.mu, like every
	// other field above. Zero hash = nothing pending. Only ever set when
	// leaderWriteAfterJournalEnabled.
	selfProposalHash types.Hash

	// writeLatchMu/writeLatches hand a per-block-hash signal from the engine
	// to the leader's own write path (internal/miner), fired once
	// journalCommitVote succeeds in tryFormPrepareQC or once advanceToView
	// abandons the view first. A separate leaf lock from e.mu -- like
	// timingMu/sendMu above -- because the miner's write path waits on the
	// returned latch OUTSIDE e.mu. See write_latch.go.
	writeLatchMu sync.Mutex
	writeLatches map[types.Hash]*writeLatch
}

// VoteJournal persists the engine's safety state. Implementations MUST make the
// record durable before returning: the engine releases a vote only after a
// successful call, which is what makes the vote recoverable across a crash.
//
// Called with the engine mutex held and on the engine's own goroutine, so an
// implementation must never call back into a locked ConsensusEngine accessor
// (deadlock) — everything it needs is in the supplied snapshot.
type VoteJournal interface {
	JournalVote(state *ConsensusState) error
}

// SetVoteJournal installs the durable vote journal. Call before Start.
func (e *ConsensusEngine) SetVoteJournal(j VoteJournal) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.voteJournal = j
}

// snapshotState builds the persistable consensus state from the round state.
// Caller must hold e.mu.
func (e *ConsensusEngine) snapshotState() *ConsensusState {
	votedView, votedHash, commitVotedView, commitVotedHash := e.roundState.VoteCommitments()
	return &ConsensusState{
		View:                e.roundState.CurrentView(),
		ConsecutiveTimeouts: e.roundState.ConsecutiveTimeouts(),
		LockedQC:            e.roundState.LockedQC().Clone(),
		LastCommittedQC:     e.roundState.LastCommittedQC().Clone(),
		LastVotedView:       votedView,
		LastVotedHash:       votedHash,
		LastCommitVotedView: commitVotedView,
		LastCommitVotedHash: commitVotedHash,
	}
}

// SnapshotState returns the persistable consensus state under the engine lock,
// for the service's periodic/commit-time persistence. Taking view, lock and
// vote commitments in one critical section keeps the persisted record internally
// consistent — a field-by-field read could otherwise store a lock from one view
// with a vote from the next.
func (e *ConsensusEngine) SnapshotState() *ConsensusState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotState()
}

// journalPrepareVote durably records a Round 1 vote commitment and, only once it
// is on disk, marks it in the round state. Returns an error when the commitment
// could not be persisted — the caller MUST then abstain: releasing a vote that
// is not on disk is exactly the failure that lets a restart equivocate.
// Caller must hold e.mu.
func (e *ConsensusEngine) journalPrepareVote(view ViewNumber, hash types.Hash) error {
	// Non-members never release a vote (sendVote gates on isMember), so there is
	// no commitment to make durable — skip the write, keep the in-memory
	// bookkeeping exactly as it was before the journal existed.
	if e.voteJournal == nil || !e.isMember() {
		e.roundState.RecordVote(view, hash)
		return nil
	}
	st := e.snapshotState()
	st.LastVotedView, st.LastVotedHash = view, hash
	// S18 (docs/QS_BLOCK_TIME_BUDGET.md 6cm): time the journal write itself,
	// diagnostic only -- no change to what is written, when, or under which
	// lock. Aggregated (summed) per view as jpvMs.
	var tJournal time.Time
	if contentionDiagEnabled {
		tJournal = time.Now()
	}
	err := e.voteJournal.JournalVote(st)
	if contentionDiagEnabled {
		e.viewTiming.Contention.journalPrepareVoteMs += time.Since(tJournal)
		e.viewTiming.Contention.journalPrepareVoteOK = true
	}
	if err != nil {
		log.Error("hotstuff: ABSTAINING — could not journal prepare vote before sending",
			"view", view, "blockHash", hash, "err", err)
		return err
	}
	e.roundState.RecordVote(view, hash)
	return nil
}

// journalCommitVote is journalPrepareVote for the Round 2 (Commit) vote.
// Caller must hold e.mu.
func (e *ConsensusEngine) journalCommitVote(view ViewNumber, hash types.Hash) error {
	if e.voteJournal == nil || !e.isMember() {
		e.roundState.RecordCommitVoteHash(view, hash)
		return nil
	}
	st := e.snapshotState()
	st.LastCommitVotedView, st.LastCommitVotedHash = view, hash
	// S18: same as journalPrepareVote above; jcvAt is the absolute start
	// time (unix ms) of this call -- on the leader this IS the self-
	// commit-vote journal write inside tryFormPrepareQC (voting.go), the
	// one call in the unstamped PrepareQCFormed->emit() gap (L0, 6cm) that
	// can block on the MDBX writer.
	var tJournal time.Time
	if contentionDiagEnabled {
		tJournal = time.Now()
	}
	err := e.voteJournal.JournalVote(st)
	if contentionDiagEnabled {
		d := time.Since(tJournal)
		e.viewTiming.Contention.journalCommitVoteMs += d
		e.viewTiming.Contention.journalCommitVoteAtMs = tJournal.UnixMilli()
		e.viewTiming.Contention.journalCommitVoteOK = true
	}
	if err != nil {
		log.Error("hotstuff: ABSTAINING — could not journal commit vote before sending",
			"view", view, "blockHash", hash, "err", err)
		return err
	}
	e.roundState.RecordCommitVoteHash(view, hash)
	return nil
}

type futureMsg struct {
	view ViewNumber
	msg  ConsensusMsg
}

// ViewTiming tracks per-view timing for latency diagnostics. Which fields are
// populated depends on this node's role in the view — see Phases() in
// view_timing.go for the derived per-stage durations.
//
//	ViewStart        every node: the view was entered (advanceToView).
//	ProposalSent     LEADER only: proposal broadcast (proposeBlock). The gap
//	                 from ViewStart is the wait for the miner's sealed block.
//	ProposalReceived FOLLOWER only: proposal verified and accepted
//	                 (processProposal), before execution is requested.
//	VoteSent         FOLLOWER only: Round 1 prepare vote sent (sendVote). Under
//	                 import-gated voting this trails ProposalReceived by the
//	                 local execution/import time. The leader self-votes inside
//	                 proposeBlock and so never sets this.
//	PrepareQCFormed  LEADER only: quorum of Round 1 votes reached
//	                 (tryFormPrepareQC), together with PrepareVoteCount.
//	CommitVoteSent   FOLLOWER only: Round 2 commit vote sent (processPrepareQC).
//	CommitQCFormed   every node: the block committed — leader via
//	                 tryFormCommitQC (also sets CommitVoteCount), follower via
//	                 processDecide (leaves CommitVoteCount at 0).
type ViewTiming struct {
	View             ViewNumber
	ViewStart        time.Time
	ProposalSent     *time.Time
	ProposalReceived *time.Time
	VoteSent         *time.Time
	PrepareQCFormed  *time.Time
	CommitVoteSent   *time.Time
	CommitQCFormed   *time.Time
	PrepareVoteCount uint32
	CommitVoteCount  uint32

	// Contention is S14's vote-path timing accumulator (diagnostic only,
	// N42_CONTENTION_DIAG=1; see contentionStamps in view_timing.go). It
	// rides along with ViewTiming so it resets/snapshots/publishes exactly
	// where the timestamps above already do, with no separate plumbing.
	Contention contentionStamps
}

func newViewTiming(view ViewNumber) ViewTiming {
	return ViewTiming{View: view, ViewStart: time.Now()}
}

// NewConsensusEngine creates a new HotStuff-2 consensus engine.
func NewConsensusEngine(
	myIndex ValidatorIndex,
	secretKey common.SecretKey,
	validatorSet *ValidatorSet,
	baseTimeoutMs, maxTimeoutMs uint64,
	outputCh chan<- EngineOutput,
) *ConsensusEngine {
	return NewConsensusEngineWithEpochManager(
		myIndex, secretKey,
		NewEpochManager(validatorSet),
		baseTimeoutMs, maxTimeoutMs,
		outputCh,
	)
}

// NewConsensusEngineWithEpochManager creates a new engine with an EpochManager.
func NewConsensusEngineWithEpochManager(
	myIndex ValidatorIndex,
	secretKey common.SecretKey,
	epochManager *EpochManager,
	baseTimeoutMs, maxTimeoutMs uint64,
	outputCh chan<- EngineOutput,
) *ConsensusEngine {
	e := &ConsensusEngine{
		myIndex:                   myIndex,
		secretKey:                 secretKey,
		epochManager:              epochManager,
		roundState:                NewRoundState(),
		pacemaker:                 NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:                  outputCh,
		importedBlocks:            make(map[types.Hash]bool),
		importedParents:           make(map[types.Hash]types.Hash),
		checkedBlocks:             make(map[types.Hash]bool),
		checkedReference:          make(map[types.Hash]types.Hash),
		pendingTxRoots:            make(map[types.Hash]types.Hash),
		pendingProposals:          make(map[ViewNumber]types.Hash),
		pendingJustifyBlocks:      make(map[ViewNumber]types.Hash),
		equivocationTracker:       make(map[ValidatorIndex]types.Hash),
		commitEquivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:           make([]futureMsg, 0),
		futureTimeouts:            make(map[ValidatorIndex]*TimeoutMessage),
	}
	// Seed the first view's timing from the round state so the very first
	// committed view is reported under its real view number.
	e.viewTiming = newViewTiming(e.roundState.CurrentView())
	e.reconfigMgr = NewReconfigurationManager(epochManager)
	return e
}

// WithRecoveredState creates an engine with recovered state from a persisted snapshot.
func WithRecoveredState(
	myIndex ValidatorIndex,
	secretKey common.SecretKey,
	epochManager *EpochManager,
	baseTimeoutMs, maxTimeoutMs uint64,
	outputCh chan<- EngineOutput,
	recoveredView ViewNumber,
	lockedQC, lastCommittedQC QuorumCertificate,
	consecutiveTimeouts uint32,
) *ConsensusEngine {
	if consecutiveTimeouts > MaxRecoveredConsecutiveTimeouts {
		log.Warn("recovered consecutive_timeouts exceeded sanity limit, capping",
			"original", consecutiveTimeouts, "capped", MaxRecoveredConsecutiveTimeouts)
		consecutiveTimeouts = MaxRecoveredConsecutiveTimeouts
	}

	e := &ConsensusEngine{
		myIndex:                   myIndex,
		secretKey:                 secretKey,
		epochManager:              epochManager,
		roundState:                RoundStateFromSnapshot(recoveredView, lockedQC, lastCommittedQC, consecutiveTimeouts),
		pacemaker:                 NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:                  outputCh,
		importedBlocks:            make(map[types.Hash]bool),
		importedParents:           make(map[types.Hash]types.Hash),
		checkedBlocks:             make(map[types.Hash]bool),
		checkedReference:          make(map[types.Hash]types.Hash),
		pendingTxRoots:            make(map[types.Hash]types.Hash),
		pendingProposals:          make(map[ViewNumber]types.Hash),
		pendingJustifyBlocks:      make(map[ViewNumber]types.Hash),
		equivocationTracker:       make(map[ValidatorIndex]types.Hash),
		commitEquivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:           make([]futureMsg, 0),
		futureTimeouts:            make(map[ValidatorIndex]*TimeoutMessage),
	}
	e.viewTiming = newViewTiming(e.roundState.CurrentView())
	e.reconfigMgr = NewReconfigurationManager(epochManager)
	return e
}

// Public accessors

func (e *ConsensusEngine) CurrentView() ViewNumber {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.roundState.CurrentView()
}

func (e *ConsensusEngine) CurrentPhase() Phase {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.roundState.Phase()
}

func (e *ConsensusEngine) Pacemaker() *Pacemaker {
	return e.pacemaker
}

// MyIndex returns this validator's index in the current validator set.
func (e *ConsensusEngine) MyIndex() ValidatorIndex {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.myIndex
}

// NonMemberIndex marks a node whose signer is not in the active validator set:
// a brand-new validator not yet added via reconfig, or one that was removed.
// Such a node still imports blocks and processes consensus messages (so its view
// advances and it can detect being added at an epoch boundary) but never
// proposes, votes, or times out. IsLeader compares against a real index and so
// is always false for it; GetAddress must never be called with it (use myAddr).
const NonMemberIndex ValidatorIndex = ^ValidatorIndex(0)

// isMember reports whether this node is in the active validator set (holds the
// engine lock via its callers). Participation gates (vote, commit-vote, timeout)
// check this so observer/removed nodes stay silent until reconfig activates them.
func (e *ConsensusEngine) isMember() bool { return e.myIndex != NonMemberIndex }

// SetSelfAddress records this node's own validator address. Required so an
// observer/removed node (myIndex==NonMemberIndex) can find itself in a newly
// applied set at an epoch boundary, where it cannot index the current set.
func (e *ConsensusEngine) SetSelfAddress(addr types.Address) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.myAddr = addr
}

// RestoreState restores persisted consensus state for crash recovery.
// Must only be called before the engine starts processing events.
func (e *ConsensusEngine) RestoreState(view ViewNumber, lockedQC, committedQC QuorumCertificate, consecutiveTimeouts uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roundState = RoundStateFromSnapshot(view, lockedQC, committedQC, consecutiveTimeouts)
	e.pacemaker.ResetForView(view, consecutiveTimeouts)
	e.viewTiming = newViewTiming(view)
	// Align the epoch counter with the recovered view. A mid-chain restart with
	// epochs enabled would otherwise leave currentEpoch at 0 while the view is far
	// ahead, decoupling historicalSets keys from ValidatorSetForView lookups.
	e.epochManager.SeedCurrentEpoch(view)
}

// RestoreVoteCommitments reinstates the journalled vote commitments after a
// restart. It must run on EVERY startup that finds a persisted record — not
// only when the recovered view is ahead — because the double-vote guard is what
// stops a node from casting a second, conflicting vote in a view it already
// voted in before the restart. Must only be called before the engine starts
// processing events.
func (e *ConsensusEngine) RestoreVoteCommitments(votedView ViewNumber, votedHash types.Hash, commitVotedView ViewNumber, commitVotedHash types.Hash) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roundState.RestoreVoteCommitments(votedView, votedHash, commitVotedView, commitVotedHash)
}

func (e *ConsensusEngine) ValidatorCount() uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.validatorSet().Len()
}

func (e *ConsensusEngine) EpochManager() *EpochManager {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager
}

// ValidatorSetForView returns the correct validator set for verifying QCs at the given view.
func (e *ConsensusEngine) ValidatorSetForView(view ViewNumber) *ValidatorSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager.ValidatorSetForView(uint64(view))
}

// ResolveQCValidatorSet is the locked, exported form of resolveQCValidatorSet for
// out-of-engine callers (e.g. header/sync QC verification in the adapter). It
// size-matches the certificate's signer bitmap against known sets so verification
// stays correct across an epoch-boundary validator-set change.
func (e *ConsensusEngine) ResolveQCValidatorSet(view ViewNumber, bitmapLen int) *ValidatorSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resolveQCValidatorSet(view, bitmapLen)
}

// StagedEpochInfoSafe returns staged epoch info under the engine lock.
func (e *ConsensusEngine) StagedEpochInfoSafe() (uint64, []ValidatorInfo, uint32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager.StagedEpochInfo()
}

// CurrentEpochInfoSafe returns the active validator set's epoch, members and fault
// tolerance under the engine lock, for persisting the reconfigured set so it
// survives a restart.
func (e *ConsensusEngine) CurrentEpochInfoSafe() (uint64, []ValidatorInfo, uint32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager.CurrentEpochInfo()
}

// RestoreValidatorSet restores a persisted active validator set after a restart and
// re-derives this node's own index in it: a node still in the set resumes as a
// member, one dropped from it becomes an observer. Without this a restarted node
// falls back to the genesis set and cannot verify QCs from the reconfigured set.
func (e *ConsensusEngine) RestoreValidatorSet(epoch uint64, validators []ValidatorInfo, f uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.epochManager.RestoreActiveSet(epoch, validators, f)
	if idx := e.epochManager.CurrentValidatorSet().FindByAddress(e.myAddr); idx >= 0 {
		e.myIndex = ValidatorIndex(idx)
		e.removed = false
	} else {
		e.myIndex = NonMemberIndex
		e.removed = true
	}
	log.Info("hotstuff: restored active validator set after restart",
		"epoch", epoch, "validators", len(validators), "myIndex", e.myIndex)
}

// RestoreStagedSet re-stages a validator set that was committed but not yet
// activated before a restart, so the node still activates it at the next epoch
// boundary. Complements RestoreValidatorSet (active set) on recovery.
func (e *ConsensusEngine) RestoreStagedSet(validators []ValidatorInfo, f uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.epochManager.StageNextEpoch(validators, f)
	log.Info("hotstuff: restored staged validator set after restart", "validators", len(validators))
}

// PreStageFromScheduleSafe stages the next epoch under the engine lock.
func (e *ConsensusEngine) PreStageFromScheduleSafe(schedule *EpochSchedule) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager.PreStageFromSchedule(schedule)
}

func (e *ConsensusEngine) IsCurrentLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return IsLeader(e.myIndex, e.roundState.CurrentView(), e.validatorSet())
}

func (e *ConsensusEngine) CurrentLeaderIndex() ValidatorIndex {
	e.mu.Lock()
	defer e.mu.Unlock()
	return LeaderForView(e.roundState.CurrentView(), e.validatorSet())
}

func (e *ConsensusEngine) LockedQC() QuorumCertificate {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.roundState.LockedQC().Clone()
}

func (e *ConsensusEngine) LastCommittedQC() QuorumCertificate {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.roundState.LastCommittedQC().Clone()
}

func (e *ConsensusEngine) ConsecutiveTimeouts() uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.roundState.ConsecutiveTimeouts()
}

// ProcessEvent processes a consensus event (thread-safe).
func (e *ConsensusEngine) ProcessEvent(event ConsensusEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Note: an observer/removed node (myIndex==NonMemberIndex) is NOT blocked here.
	// It must process messages and imported blocks so its view advances and it can
	// detect being added at an epoch boundary. Participation is gated downstream:
	// onBlockReady via IsLeader, sendVote/sendCommitVote and the timeout paths via
	// isMember. This is what lets a brand-new validator bootstrap and a removed one
	// rejoin without a process restart.

	switch event.Type {
	case EventMessage:
		return e.processMessage(event.Msg, msgTiming{arrive: event.ReceivedAt, rx: event.RxAt, via: event.Via})
	case EventBlockReady:
		return e.onBlockReady(event.Hash, event.TxRootHash)
	case EventBlockImported:
		return e.onBlockImported(event.Hash, event.TxRootHash, event.ParentHash)
	case EventBlockChecked:
		return e.onBlockChecked(event.Hash, event.ParentHash, event.ReferenceHash)
	case EventBlockRejected:
		e.onBlockRejected(event.Hash)
		return nil
	case EventBlockHeaderKnown:
		return e.onBlockHeaderKnown(event.Hash, event.ParentHash, event.Number)
	default:
		return nil
	}
}

// OnTimeout handles a view timeout triggered by the pacemaker (thread-safe).
func (e *ConsensusEngine) OnTimeout() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.isMember() {
		return nil // observer/removed nodes do not drive timeouts
	}
	return e.onTimeout()
}

// SetRemoved marks this engine as removed from the validator set.
func (e *ConsensusEngine) SetRemoved() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removed = true
	e.pacemaker.Stop()
}

// IsRemoved returns whether this validator has been removed.
func (e *ConsensusEngine) IsRemoved() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.removed
}

// ConsensusEvent represents events fed into the consensus engine.
type ConsensusEvent struct {
	Type       ConsensusEventType
	Msg        ConsensusMsg
	Hash       types.Hash
	TxRootHash types.Hash // DA commitment: transaction root hash (Baby Raptr)
	ParentHash types.Hash // EventBlockImported / EventBlockChecked / EventBlockHeaderKnown: the block's parent (extends-check; zero = unknown, check skipped)
	Number     uint64     // EventBlockHeaderKnown: the block's own height, for logging only (extendsJustify never reads it)
	// ReferenceHash (S55, 6f7): EventBlockChecked only. The ancestor
	// deferredAttested requires imported, when depth-2 is active for this
	// block (the grandparent) -- zero under depth-1, where ParentHash alone
	// already serves both extendsJustify and deferredAttested.
	ReferenceHash types.Hash
	// ReceivedAt is S14's diagnostic arrival stamp for an EventMessage: the
	// first line of the network handler (processGossipMessage), before
	// decode. Zero unless N42_CONTENTION_DIAG=1; every downstream contention
	// stamp is gated on it being non-zero, so leaving it unset costs nothing.
	ReceivedAt time.Time
	// RxAt is S17's diagnostic t_rx: the EARLIEST point this message's bytes
	// were in this process -- right after sub.Next() returns (gossip) or
	// right after the Rotor stream read completes (direct/relay) -- taken
	// BEFORE ReceivedAt (which is after snappy/RLP decode). Via names which
	// transport delivered THIS copy ("rotor" | "gossip"). Both zero/empty
	// unless N42_CONTENTION_DIAG=1.
	RxAt time.Time
	Via  string
}

// msgTiming bundles S14/S17's diagnostic inbound timestamps for one
// consensus message as it flows from processMessage down to the process*
// handler that runs under e.mu -- a small value type instead of three
// separate parameters threaded through processMessage/dispatchMessage/
// processProposal/processVote/processCommitVote/processPrepareQC. Zero
// value (Arrive.IsZero()) means "not measured," which every recorder
// already checks.
type msgTiming struct {
	arrive time.Time // t_arrive (S14): first line of processGossipMessage, before decode
	rx     time.Time // t_rx (S17): earliest point the bytes were in-process
	via    string    // "rotor" | "gossip": which transport delivered THIS copy
}

// ConsensusEventType identifies the type of consensus event.
type ConsensusEventType uint8

const (
	EventMessage       ConsensusEventType = 1
	EventBlockReady    ConsensusEventType = 2
	EventBlockImported ConsensusEventType = 3
	// EventBlockChecked: deferred execution -- the block's header carries this
	// node's result of its parent and its transactions are includable; the
	// vote no longer waits for the block's own import, only for the parent's.
	EventBlockChecked ConsensusEventType = 4
	// EventBlockRejected: the block failed validation on import; any
	// deferred-execution check evidence for it is withdrawn.
	EventBlockRejected ConsensusEventType = 5
	// EventBlockHeaderKnown (S31, docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh):
	// the block-push receive path has decoded (peeked, when the reader
	// allows it) this block's HEADER -- its parent hash is now known, well
	// before the (possibly 160k-transaction) body finishes decoding and
	// before CheckDeferredBlock's own per-transaction walk runs. This is
	// enough for extendsJustify (which only ever reads the parent hash) to
	// evaluate, so a two-phase Round 1 prepare vote can fire on it directly
	// -- Round 2's own execution guarantee (deferredAttested / the full
	// import gate) is completely unchanged and still waits for the real
	// check. Two-phase only: for import-gated (non-two-phase) voting the
	// event still records the parent (harmless, already-covered
	// bookkeeping) but never by itself unlocks a vote.
	EventBlockHeaderKnown ConsensusEventType = 6
)

// Internal helpers

func (e *ConsensusEngine) validatorSet() *ValidatorSet {
	return e.epochManager.CurrentValidatorSet()
}

// validatorSetForView returns the validator set that was active at the given view
// — the set that would have signed a QC/TC for it. Use this (not validatorSet)
// for verifying any certificate that may predate the current set, so verification
// stays correct across an epoch-boundary validator-set change.
func (e *ConsensusEngine) validatorSetForView(view ViewNumber) *ValidatorSet {
	return e.epochManager.ValidatorSetForView(uint64(view))
}

// resolveQCValidatorSet returns the validator set to verify a certificate with
// bitmapLen signer slots that was formed at the given view. It first tries the
// view-derived set; if its size matches the bitmap it is used directly. On a size
// mismatch (epoch drift — e.g. a certificate carried across a validator-set change
// whose view no longer maps cleanly), it falls back to the same-sized known set.
// The subsequent BLS aggregate check confirms correctness, so this can only ever
// pick the RIGHT set or fail the signature — never admit a forged certificate.
func (e *ConsensusEngine) resolveQCValidatorSet(view ViewNumber, bitmapLen int) *ValidatorSet {
	primary := e.validatorSetForView(view)
	if primary != nil && int(primary.Len()) == bitmapLen {
		return primary
	}
	if vs := e.epochManager.FindValidatorSetByLen(uint32(bitmapLen)); vs != nil {
		return vs
	}
	return primary
}

// SetTwoPhaseVote switches between classic import-gated Round-1 voting
// (false) and two-phase voting (true): R1 on static validation, R2 gated on
// local import. Call before Start; thread-safe.
func (e *ConsensusEngine) SetTwoPhaseVote(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.twoPhaseVote = on
}

// CurrentValidatorSet returns the active validator set (thread-safe).
func (e *ConsensusEngine) CurrentValidatorSet() *ValidatorSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.validatorSet()
}

// ReconfigManager returns the reconfiguration manager for proposing
// validator set changes. The returned manager is safe for concurrent use
// only when accessed through the engine's lock (via ProcessEvent).
func (e *ConsensusEngine) ReconfigManager() *ReconfigurationManager {
	return e.reconfigMgr
}

func (e *ConsensusEngine) emit(output EngineOutput) error {
	if contentionDiagEnabled {
		output.EmittedAt = time.Now()
	}
	select {
	case e.outputCh <- output:
		return nil
	default:
	}
	// Channel full — log and drop. Never sleep with engine lock held.
	if isCriticalOutput(output.Type) {
		log.Error("CRITICAL: consensus output channel full, dropping", "type", output.Type, "view", e.roundState.CurrentView())
	}
	mxOutputDrops.Inc()
	log.Warn("consensus output dropped (channel full)", "type", output.Type, "view", e.roundState.CurrentView())
	return ErrOutputChannelClosed
}

func isCriticalOutput(t EngineOutputType) bool {
	return t == OutputBlockCommitted || t == OutputBroadcast ||
		t == OutputSendToValidator || t == OutputEquivocationDetected ||
		t == OutputEpochTransition
}

// sendStampsMaxViews bounds ConsensusEngine.sendByView: a view whose
// message is never published (a timed-out view, a dropped output) or
// whose record is never claimed by publishCommittedTiming leaves an entry
// behind. Pruned oldest-first once the map exceeds this, under sendMu.
const sendStampsMaxViews = 64

// recordSendStamp is S17's sender-side timing recorder, called from
// handleBroadcast/handleSendToValidator's background goroutines (Service,
// service.go) after the actual publish completes -- i.e. AFTER the output
// already left the engine via emit(), on a goroutine that never holds
// e.mu. Guarded by sendMu, a leaf lock (see the struct field comment):
// never held while e.mu is held, and e.mu is never acquired while this is
// held, so it cannot contend with or deadlock against the consensus hot
// path. No-op unless the diagnostic is on.
func (e *ConsensusEngine) recordSendStamp(view ViewNumber, msgType ConsensusMsgType, emit, deq, pub0, pub1 time.Time, path string) {
	// No contentionDiagEnabled check here: both call sites (handleBroadcast,
	// handleSendToValidator) already gate on it before ever computing a
	// non-zero emit, so this stays a pure function of its arguments --
	// straightforward to unit test without the env-gated package var.
	if emit.IsZero() {
		return
	}
	stamp := sendMsgStamp{
		emit2Deq: durSince(emit, deq),
		deq2Pub:  durSince(deq, pub0),
		pubDur:   durSince(pub0, pub1),
		pubAtMs:  pub1.UnixMilli(),
		path:     path,
		ok:       true,
	}
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	if e.sendByView == nil {
		e.sendByView = make(map[ViewNumber]*perViewSendStamps)
	}
	rec, ok := e.sendByView[view]
	if !ok {
		if len(e.sendByView) >= sendStampsMaxViews {
			e.pruneOldestSendStampLocked()
		}
		rec = &perViewSendStamps{}
		e.sendByView[view] = rec
	}
	switch msgType {
	case MsgProposal:
		rec.proposal = stamp
	case MsgVote:
		rec.prepareVote = stamp
	case MsgPrepareQC:
		rec.prepareQC = stamp
	case MsgCommitVote:
		rec.commitVote = stamp
	}
}

// pruneOldestSendStampLocked drops the lowest-numbered view's entry.
// Caller must hold sendMu. Views only increase, so "lowest" is "oldest."
func (e *ConsensusEngine) pruneOldestSendStampLocked() {
	var oldest ViewNumber
	first := true
	for v := range e.sendByView {
		if first || v < oldest {
			oldest, first = v, false
		}
	}
	if !first {
		delete(e.sendByView, oldest)
	}
}

// takeSendStamps claims (pops) a view's sender-side stamps, called once
// from publishCommittedTiming when that view commits. Returns nil if no
// message this node sent for that view has finished publishing yet (a
// slow publish that outlives the view is simply not attributed -- see
// docs/QS_BLOCK_TIME_BUDGET.md 6cl's method note).
func (e *ConsensusEngine) takeSendStamps(view ViewNumber) *perViewSendStamps {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	rec := e.sendByView[view]
	delete(e.sendByView, view)
	return rec
}

// durSince is time.Time.Sub with a floor of zero, guarding against a
// non-monotonic pair (clock adjustment) producing a negative duration.
func durSince(earlier, later time.Time) time.Duration {
	d := later.Sub(earlier)
	if d < 0 {
		return 0
	}
	return d
}

func (e *ConsensusEngine) advanceToView(newView ViewNumber) error {
	if newView <= e.roundState.CurrentView() {
		return nil
	}

	// S19 (6co): the view this node (possibly as leader) is about to leave is
	// ending, one way or another. If it proposed a block that never reached
	// journalCommitVote (a timeout: PrepareQC never formed, so tryFormPrepareQC
	// never got there), release any write latch waiting on it now rather than
	// making the write path sit out its full configured timeout. On the
	// ordinary success path this block's latch was already fired ("journal")
	// before advanceToView is ever reached here (voting.go's tryFormCommitQC
	// calls advanceToView AFTER emitting OutputBlockCommitted, which itself
	// only runs once CommitQC formed, which requires PrepareQC to have formed
	// first) -- so this is a harmless, idempotent no-op fire in that case
	// (writeLatch.Fire: first writer wins). No-op entirely when the switch is
	// off: selfProposalHash is only ever set when it is on.
	if leaderWriteAfterJournalEnabled && e.selfProposalHash != (types.Hash{}) {
		e.fireWriteLatch(e.selfProposalHash, "abandoned")
		e.selfProposalHash = types.Hash{}
	}

	epochBoundary := e.epochManager.EpochsEnabled() && e.epochManager.IsEpochBoundary(newView)

	// Save current PrepareQC for piggybacking.
	e.previousPrepareQC = e.prepareQC

	// Check epoch boundary — apply pending reconfigurations if committed.
	if e.epochManager.EpochsEnabled() && e.epochManager.IsEpochBoundary(newView) {
		// The next-epoch validator set was already staged at CommitQC time
		// (ReconfigurationManager.MarkCommitted -> EpochManager.StageNextEpoch), which
		// is precisely what makes it visible to QC verification BEFORE this boundary —
		// a catching-up node verifies post-boundary blocks against the staged set via
		// resolveQCValidatorSet/FindValidatorSetByLen. Here we only ACTIVATE it.
		if e.epochManager.HasStagedNext() {
			if err := e.emit(EngineOutput{
				Type:           OutputEpochStaged,
				NewEpoch:       e.epochManager.CurrentEpoch() + 1,
				ValidatorCount: e.epochManager.PeekNextSet().Len(),
			}); err != nil {
				return err
			}
		}

		if e.epochManager.AdvanceEpoch(uint64(newView)) {
			// The set changed. Re-derive our own index in the now-active set. This also
			// covers a node that joined/rejoined via block sync: without it the node
			// keeps its stale observer index and fails BLS verification on every signed
			// message it emits. myIndex may be NonMemberIndex here (an observer being
			// added), so match on the stored self address, not a set index.
			myAddr := e.myAddr
			newIdx := e.validatorSet().FindByAddress(myAddr)
			switch {
			case newIdx >= 0:
				wasInactive := !e.isMember()
				e.myIndex = ValidatorIndex(newIdx)
				if wasInactive {
					// Just added (fresh bootstrap or rejoin after removal): resume
					// participation. removed is cleared and the pacemaker is reset for
					// the current view by the ResetForView at the end of advanceToView.
					e.removed = false
					log.Info("This node added to validator set at epoch boundary",
						"address", myAddr.Hex(), "index", newIdx, "epoch", e.epochManager.CurrentEpoch())
				}
			case e.isMember():
				// Was an active member, now removed from the set.
				log.Warn("This node removed from validator set at epoch boundary",
					"address", myAddr.Hex(), "epoch", e.epochManager.CurrentEpoch())
				_ = e.emit(EngineOutput{Type: OutputEpochTransition, Removed: true})
				e.removed = true
				e.myIndex = NonMemberIndex // stay silent but keep processing to detect re-add
				e.pacemaker.Stop()
				return nil // stop consensus participation immediately
				// default (already inactive and still not in the set): stay an observer.
			}

			newEpoch := e.epochManager.CurrentEpoch()
			validatorCount := e.validatorSet().Len()
			log.Info("epoch transition at view boundary",
				"epoch", newEpoch,
				"validators", validatorCount,
				"quorum", e.validatorSet().QuorumSize(),
				"view", newView,
			)
			if err := e.emit(EngineOutput{
				Type:           OutputEpochTransition,
				NewEpoch:       newEpoch,
				ValidatorCount: validatorCount,
			}); err != nil {
				return err
			}
		}
	}

	e.roundState.AdvanceView(newView)
	e.pacemaker.ResetForView(newView, e.roundState.ConsecutiveTimeouts())
	e.voteCollector = nil
	e.commitCollector = nil
	e.timeoutCollector = nil
	e.prepareQC = nil
	e.pendingCommitQC = nil // a held two-phase CommitVote is view-scoped
	// Keep importedBlocks/importedFIFO across the view change: a block we have
	// imported we still have, and the next leader typically RE-proposes the same
	// (uncommitted) block in the new view. Wiping it made the re-proposed block
	// look un-imported, so processProposal deferred the vote — but the block was
	// already imported and never re-emits an import event, so the vote was never
	// cast and the round could only ever time out (import-gated-vote deadlock).
	// Drop only pending proposals for views we have already left (they are stale;
	// a vote for a past view is rejected by the leader anyway).
	for v := range e.pendingProposals {
		if v < newView {
			delete(e.pendingProposals, v)
		}
	}
	for v := range e.pendingJustifyBlocks {
		if v < newView {
			delete(e.pendingJustifyBlocks, v)
		}
	}
	e.equivocationTracker = make(map[ValidatorIndex]types.Hash)
	e.commitEquivocationTracker = make(map[ValidatorIndex]types.Hash)
	e.prepareVoteBuf = e.prepareVoteBuf[:0]
	e.commitVoteBuf = e.commitVoteBuf[:0]
	// Retain verified observations above the new view. Timeout payloads are
	// deterministic and GossipSub suppresses repeat publications, so clearing
	// them here can strand a node after the first weak jump. Validator indexes
	// can change at an epoch boundary, however, so observations verified under
	// the old set must not survive that transition.
	if epochBoundary {
		e.futureTimeouts = make(map[ValidatorIndex]*TimeoutMessage)
	} else {
		for sender, observed := range e.futureTimeouts {
			if observed.View <= newView {
				delete(e.futureTimeouts, sender)
			}
		}
	}

	// Preserve timing from committed view, publish it to observers, and export
	// the per-stage breakdown (metrics + one compact log line). Timed-out views
	// never reach a CommitQC and are therefore not sampled.
	if e.viewTiming.CommitQCFormed != nil {
		e.publishCommittedTiming(e.viewTiming)
	}
	e.viewTiming = newViewTiming(newView)

	// Replay buffered messages for the new view.
	drained := e.futureMsgBuffer
	e.futureMsgBuffer = make([]futureMsg, 0)
	var toReplay []ConsensusMsg
	for _, fm := range drained {
		if fm.view == newView {
			toReplay = append(toReplay, fm.msg)
		} else if fm.view > newView && fm.view <= newView+FutureViewWindow {
			e.futureMsgBuffer = append(e.futureMsgBuffer, fm)
		}
	}

	for _, msg := range toReplay {
		// time.Time{}: a replayed future-buffered message's original network
		// arrival time is not retained in futureMsg, so its contention stamps
		// (S14) are correctly left unmeasured rather than misattributed.
		if err := e.dispatchMessage(msg, msgTiming{}); err != nil {
			log.Debug("buffered message replay failed", "view", newView, "err", err)
		}
	}

	return nil
}

// Message processing

func (e *ConsensusEngine) processMessage(msg ConsensusMsg, mt msgTiming) error {
	// SyncInfo: adopt any piggybacked TC before view gating, so a lagging node
	// catches up from a vote/timeout even if it missed the NewView.
	if err := e.processEmbeddedTC(&msg); err != nil {
		return err
	}

	msgView := messageView(msg)
	currentView := e.roundState.CurrentView()

	// Buffer future messages within window. Decide and NewView are exempt.
	if msgView > 0 {
		isExempt := msg.Type == MsgDecide || msg.Type == MsgNewView ||
			(msg.Type == MsgTimeout && msgView <= currentView+FutureViewWindow)

		if msgView > currentView && !isExempt {
			if msgView <= currentView+FutureViewWindow {
				if len(e.futureMsgBuffer) >= MaxFutureMessages {
					// Evict lowest view entry.
					minIdx := 0
					for i, fm := range e.futureMsgBuffer {
						if fm.view < e.futureMsgBuffer[minIdx].view {
							minIdx = i
						}
					}
					e.futureMsgBuffer[minIdx] = e.futureMsgBuffer[len(e.futureMsgBuffer)-1]
					e.futureMsgBuffer = e.futureMsgBuffer[:len(e.futureMsgBuffer)-1]
				}
				e.futureMsgBuffer = append(e.futureMsgBuffer, futureMsg{view: msgView, msg: msg})
				return nil
			}

			// Beyond window: attempt QC-based view jump.
			if jumped, err := e.tryQCViewJump(&msg, msgView); err != nil {
				return err
			} else if jumped {
				newCurrent := e.roundState.CurrentView()
				if msgView == newCurrent {
					return e.dispatchMessage(msg, mt)
				} else if msgView > newCurrent && msgView <= newCurrent+FutureViewWindow &&
					len(e.futureMsgBuffer) < MaxFutureMessages {
					e.futureMsgBuffer = append(e.futureMsgBuffer, futureMsg{view: msgView, msg: msg})
				}
			}
			return nil
		}
	}

	// Discard stale messages (except Decide/NewView).
	if msgView > 0 && msgView < currentView && msg.Type != MsgDecide && msg.Type != MsgNewView {
		return nil
	}

	return e.dispatchMessage(msg, mt)
}

// dispatchMessage routes a decoded consensus message to its handler. mt is
// S14/S17's diagnostic arrival timing (zero unless N42_CONTENTION_DIAG=1,
// or for a replayed future-buffered message whose original timing is not
// retained -- see advanceToView).
func (e *ConsensusEngine) dispatchMessage(msg ConsensusMsg, mt msgTiming) error {
	if msg.Payload == nil {
		return ErrInvalidMessage
	}
	switch msg.Type {
	case MsgProposal:
		p, ok := msg.Payload.(*Proposal)
		if !ok || p == nil {
			return ErrInvalidMessage
		}
		return e.processProposal(p, mt)
	case MsgVote:
		v, ok := msg.Payload.(*Vote)
		if !ok || v == nil {
			return ErrInvalidMessage
		}
		return e.processVote(v, mt)
	case MsgCommitVote:
		cv, ok := msg.Payload.(*CommitVote)
		if !ok || cv == nil {
			return ErrInvalidMessage
		}
		return e.processCommitVote(cv, mt)
	case MsgPrepareQC:
		pqc, ok := msg.Payload.(*PrepareQCMsg)
		if !ok || pqc == nil {
			return ErrInvalidMessage
		}
		return e.processPrepareQC(pqc, mt)
	case MsgTimeout:
		tm, ok := msg.Payload.(*TimeoutMessage)
		if !ok || tm == nil {
			return ErrInvalidMessage
		}
		return e.processTimeout(tm)
	case MsgNewView:
		nv, ok := msg.Payload.(*NewViewMsg)
		if !ok || nv == nil {
			return ErrInvalidMessage
		}
		return e.processNewView(nv)
	case MsgDecide:
		d, ok := msg.Payload.(*Decide)
		if !ok || d == nil {
			return ErrInvalidMessage
		}
		return e.processDecide(d)
	}
	return ErrInvalidMessage
}

func messageView(msg ConsensusMsg) ViewNumber {
	if msg.Payload == nil {
		return 0
	}
	switch msg.Type {
	case MsgProposal:
		if p, ok := msg.Payload.(*Proposal); ok && p != nil {
			return p.View
		}
	case MsgVote:
		if v, ok := msg.Payload.(*Vote); ok && v != nil {
			return v.View
		}
	case MsgCommitVote:
		if cv, ok := msg.Payload.(*CommitVote); ok && cv != nil {
			return cv.View
		}
	case MsgPrepareQC:
		if pqc, ok := msg.Payload.(*PrepareQCMsg); ok && pqc != nil {
			return pqc.View
		}
	case MsgTimeout:
		if tm, ok := msg.Payload.(*TimeoutMessage); ok && tm != nil {
			return tm.View
		}
	case MsgNewView:
		if nv, ok := msg.Payload.(*NewViewMsg); ok && nv != nil {
			return nv.View
		}
	case MsgDecide:
		if d, ok := msg.Payload.(*Decide); ok && d != nil {
			return d.View
		}
	}
	return 0
}

func extractQCFromMessage(msg *ConsensusMsg) *QuorumCertificate {
	if msg == nil || msg.Payload == nil {
		return nil
	}
	switch msg.Type {
	case MsgProposal:
		if p, ok := msg.Payload.(*Proposal); ok && p != nil {
			qc := p.JustifyQC
			return &qc
		}
	case MsgTimeout:
		if tm, ok := msg.Payload.(*TimeoutMessage); ok && tm != nil {
			qc := tm.HighQC
			return &qc
		}
	case MsgDecide:
		if d, ok := msg.Payload.(*Decide); ok && d != nil {
			qc := d.CommitQC
			return &qc
		}
	case MsgPrepareQC:
		if pqc, ok := msg.Payload.(*PrepareQCMsg); ok && pqc != nil {
			qc := pqc.QC
			return &qc
		}
	case MsgNewView:
		if nv, ok := msg.Payload.(*NewViewMsg); ok && nv != nil {
			qc := nv.TimeoutCert.HighQC
			return &qc
		}
	}
	return nil
}

// extractHighTCFromMessage returns the piggybacked (SyncInfo) TC carried on a
// vote/commit-vote/timeout message, or nil if none. NewView carries its TC on
// its own dedicated path (processNewView) and is intentionally excluded here.
func extractHighTCFromMessage(msg *ConsensusMsg) *TimeoutCertificate {
	if msg == nil || msg.Payload == nil {
		return nil
	}
	switch msg.Type {
	case MsgVote:
		if v, ok := msg.Payload.(*Vote); ok && v != nil {
			return v.HighTC
		}
	case MsgCommitVote:
		if cv, ok := msg.Payload.(*CommitVote); ok && cv != nil {
			return cv.HighTC
		}
	case MsgTimeout:
		if tm, ok := msg.Payload.(*TimeoutMessage); ok && tm != nil {
			return tm.HighTC
		}
	}
	return nil
}

// processEmbeddedTC implements the SyncInfo view-sync rule (Jolteon/Aptos): a
// TC piggybacked on any vote/timeout is the network's proof that view tc.View
// completed, so a lagging validator adopts it immediately — advancing to
// tc.View+1 and locking on tc.HighQC — without waiting for a NewView it may
// have missed. Runs before the view-gated buffering in processMessage so the
// jump happens even for far-future messages. Safety is unaffected: advancing
// views never violates the voting rule.
func (e *ConsensusEngine) processEmbeddedTC(msg *ConsensusMsg) error {
	tc := extractHighTCFromMessage(msg)
	if tc == nil || tc.View == 0 {
		return nil
	}

	currentView := e.roundState.CurrentView()
	targetView := tc.View + 1

	// Nothing to gain if the TC doesn't move us forward. Still record it (if
	// higher than what we hold) so we keep re-propagating the freshest TC.
	if targetView <= currentView {
		e.roundState.UpdateHighestTC(tc)
		return nil
	}

	// Verify the TC and its embedded high_qc before acting on it.
	if err := e.verifyTCWithSet(tc, e.resolveQCValidatorSet(tc.View, len(tc.Signers))); err != nil {
		log.Debug("ignoring piggybacked TC with invalid signature", "tcView", tc.View, "err", err)
		return nil
	}
	if err := e.verifyEmbeddedQC(&tc.HighQC); err != nil {
		log.Debug("ignoring piggybacked TC with invalid high_qc", "tcView", tc.View, "err", err)
		return nil
	}

	log.Info("SyncInfo TC view jump: catching up to network",
		"currentView", currentView, "targetView", targetView, "tcView", tc.View)

	e.roundState.UpdateHighestTC(tc)
	e.roundState.UpdateLockedQC(&tc.HighQC)
	e.roundState.ResetConsecutiveTimeouts()

	if err := e.advanceToView(targetView); err != nil {
		return err
	}
	return e.emit(EngineOutput{Type: OutputViewChanged, View: e.roundState.CurrentView()})
}

func (e *ConsensusEngine) tryQCViewJump(msg *ConsensusMsg, msgView ViewNumber) (bool, error) {
	currentView := e.roundState.CurrentView()

	qc := extractQCFromMessage(msg)
	if qc == nil || qc.View == 0 {
		return false, nil
	}

	// Verify QC. Decide uses commit signing domain.
	var verifyErr error
	if msg.Type == MsgDecide {
		verifyErr = e.verifyCommitQCWithSet(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
	} else {
		verifyErr = e.verifyQCWithSet(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
	}
	if verifyErr != nil {
		return false, nil
	}

	// A QC only proves the network reached qc.View, so the highest view it
	// can justify advancing to is qc.View+1. Do NOT bump to msgView: this
	// path runs before the message's own signature/proposer is verified
	// (dispatchMessage → processProposal happens later), and msgView is
	// fully attacker-controlled. Trusting it would let a single node wrap a
	// real, public QC in a message carrying an arbitrarily high view and
	// push any peer's view permanently to that height (a liveness DoS, and
	// persisted across restarts) — the same unbounded view-jump the H2 fix
	// closed on the timeout path. Higher jumps must be proven by a TC via
	// processEmbeddedTC, never by a bare view number.
	targetView := qc.View + 1
	if targetView <= currentView {
		return false, nil
	}

	log.Info("QC-based view jump: recovering node catching up to network",
		"currentView", currentView, "targetView", targetView, "qcView", qc.View)

	e.roundState.UpdateLockedQC(qc)
	e.roundState.ResetConsecutiveTimeouts()

	if err := e.emit(EngineOutput{
		Type:       OutputSyncRequired,
		LocalView:  currentView,
		TargetView: targetView,
	}); err != nil {
		return false, err
	}

	if err := e.advanceToView(targetView); err != nil {
		return false, err
	}

	actualView := e.roundState.CurrentView()
	if err := e.emit(EngineOutput{Type: OutputViewChanged, View: actualView}); err != nil {
		return false, err
	}

	return true, nil
}

// verifyEmbeddedQC verifies a QC embedded in a timeout or NewView message.
// Genesis QC (view 0) is exempt.
func (e *ConsensusEngine) verifyEmbeddedQC(qc *QuorumCertificate) error {
	if qc.View == 0 {
		return nil
	}
	return e.verifyQCAnyDomainWithSet(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
}
