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
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/log"
)

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
	importedBlocks   map[types.Hash]bool
	importedParents  map[types.Hash]types.Hash // imported blockHash → its parent hash (extends-check at vote time)
	importedFIFO     []types.Hash              // insertion order for bounded eviction; blocks stay known-imported across view changes
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

	// Set when this validator is removed at an epoch boundary.
	removed bool
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
		return e.processMessage(event.Msg)
	case EventBlockReady:
		return e.onBlockReady(event.Hash, event.TxRootHash)
	case EventBlockImported:
		return e.onBlockImported(event.Hash, event.TxRootHash, event.ParentHash)
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
	ParentHash types.Hash // EventBlockImported only: imported block's parent (extends-check; zero = unknown, check skipped)
}

// ConsensusEventType identifies the type of consensus event.
type ConsensusEventType uint8

const (
	EventMessage       ConsensusEventType = 1
	EventBlockReady    ConsensusEventType = 2
	EventBlockImported ConsensusEventType = 3
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

func (e *ConsensusEngine) advanceToView(newView ViewNumber) error {
	if newView <= e.roundState.CurrentView() {
		return nil
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
		if err := e.dispatchMessage(msg); err != nil {
			log.Debug("buffered message replay failed", "view", newView, "err", err)
		}
	}

	return nil
}

// Message processing

func (e *ConsensusEngine) processMessage(msg ConsensusMsg) error {
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
					return e.dispatchMessage(msg)
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

	return e.dispatchMessage(msg)
}

func (e *ConsensusEngine) dispatchMessage(msg ConsensusMsg) error {
	if msg.Payload == nil {
		return ErrInvalidMessage
	}
	switch msg.Type {
	case MsgProposal:
		p, ok := msg.Payload.(*Proposal)
		if !ok || p == nil {
			return ErrInvalidMessage
		}
		return e.processProposal(p)
	case MsgVote:
		v, ok := msg.Payload.(*Vote)
		if !ok || v == nil {
			return ErrInvalidMessage
		}
		return e.processVote(v)
	case MsgCommitVote:
		cv, ok := msg.Payload.(*CommitVote)
		if !ok || cv == nil {
			return ErrInvalidMessage
		}
		return e.processCommitVote(cv)
	case MsgPrepareQC:
		pqc, ok := msg.Payload.(*PrepareQCMsg)
		if !ok || pqc == nil {
			return ErrInvalidMessage
		}
		return e.processPrepareQC(pqc)
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
	if err := VerifyTC(tc, e.resolveQCValidatorSet(tc.View, len(tc.Signers))); err != nil {
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
		verifyErr = VerifyCommitQC(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
	} else {
		verifyErr = VerifyQC(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
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
	return VerifyQCAnyDomain(qc, e.resolveQCValidatorSet(qc.View, len(qc.Signers)))
}
