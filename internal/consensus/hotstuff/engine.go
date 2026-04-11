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

	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/common/types"
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
	secretKey common.SecretKey

	epochManager  *EpochManager
	reconfigMgr   *ReconfigurationManager
	roundState    *RoundState
	pacemaker     *Pacemaker

	voteCollector    *VoteCollector
	commitCollector  *VoteCollector
	timeoutCollector *TimeoutCollector

	prepareQC         *QuorumCertificate
	previousPrepareQC *QuorumCertificate

	// Output channel for actions requested from the outer node.
	outputCh chan<- EngineOutput

	// Block tracking
	importedBlocks     map[types.Hash]bool
	pendingTxRoots     map[types.Hash]types.Hash // blockHash → expected TxRootHash (DA verification)
	equivocationTracker       map[ValidatorIndex]types.Hash
	commitEquivocationTracker map[ValidatorIndex]types.Hash

	// Batch BLS verification buffers (votes await batch pairing before
	// being added to the collector). Reset on every view change.
	prepareVoteBuf []pendingVote
	commitVoteBuf  []pendingVote

	// Future message buffer
	futureMsgBuffer []futureMsg

	// Timing
	viewTiming         ViewTiming
	lastCommittedTiming *ViewTiming

	// Set when this validator is removed at an epoch boundary.
	removed bool
}

type futureMsg struct {
	view ViewNumber
	msg  ConsensusMsg
}

// ViewTiming tracks per-view timing for latency diagnostics.
type ViewTiming struct {
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

func newViewTiming() ViewTiming {
	return ViewTiming{ViewStart: time.Now()}
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
		myIndex:             myIndex,
		secretKey:           secretKey,
		epochManager:        epochManager,
		roundState:          NewRoundState(),
		pacemaker:           NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:            outputCh,
		importedBlocks:      make(map[types.Hash]bool),
		pendingTxRoots:      make(map[types.Hash]types.Hash),
		equivocationTracker: make(map[ValidatorIndex]types.Hash),
		commitEquivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:     make([]futureMsg, 0),
		viewTiming:          newViewTiming(),
	}
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
		myIndex:             myIndex,
		secretKey:           secretKey,
		epochManager:        epochManager,
		roundState:          RoundStateFromSnapshot(recoveredView, lockedQC, lastCommittedQC, consecutiveTimeouts),
		pacemaker:           NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:            outputCh,
		importedBlocks:      make(map[types.Hash]bool),
		pendingTxRoots:      make(map[types.Hash]types.Hash),
		equivocationTracker: make(map[ValidatorIndex]types.Hash),
		commitEquivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:     make([]futureMsg, 0),
		viewTiming:          newViewTiming(),
	}
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

// RestoreState restores persisted consensus state for crash recovery.
// Must only be called before the engine starts processing events.
func (e *ConsensusEngine) RestoreState(view ViewNumber, lockedQC, committedQC QuorumCertificate, consecutiveTimeouts uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roundState = RoundStateFromSnapshot(view, lockedQC, committedQC, consecutiveTimeouts)
	e.pacemaker.ResetForView(view, consecutiveTimeouts)
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

// StagedEpochInfoSafe returns staged epoch info under the engine lock.
func (e *ConsensusEngine) StagedEpochInfoSafe() (uint64, []ValidatorInfo, uint32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochManager.StagedEpochInfo()
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
	if e.removed {
		return ErrValidatorRemoved
	}

	switch event.Type {
	case EventMessage:
		return e.processMessage(event.Msg)
	case EventBlockReady:
		return e.onBlockReady(event.Hash, event.TxRootHash)
	case EventBlockImported:
		return e.onBlockImported(event.Hash, event.TxRootHash)
	default:
		return nil
	}
}

// OnTimeout handles a view timeout triggered by the pacemaker (thread-safe).
func (e *ConsensusEngine) OnTimeout() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.removed {
		return ErrValidatorRemoved
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

	// Save current PrepareQC for piggybacking.
	e.previousPrepareQC = e.prepareQC

	// Check epoch boundary — apply pending reconfigurations if committed.
	if e.epochManager.EpochsEnabled() && e.epochManager.IsEpochBoundary(newView) {
		// Apply committed reconfiguration changes before advancing epoch.
		// Per HotStuff-2 § 5: changes take effect only after the old set commits them.
		// Note: ApplyAtEpochBoundary() validates internally (ValidateTransition)
		// and only stages the set if validation passes.
		if e.reconfigMgr != nil && e.reconfigMgr.IsCommitted() {
			// Capture own address before the set changes
			myAddr, _ := e.validatorSet().GetAddress(e.myIndex)

			if newSet := e.reconfigMgr.ApplyAtEpochBoundary(); newSet != nil {
				// Emit staged event so Service persists it for crash recovery.
				if err := e.emit(EngineOutput{
					Type:           OutputEpochStaged,
					NewEpoch:       e.epochManager.CurrentEpoch() + 1,
					ValidatorCount: newSet.Len(),
				}); err != nil {
					return err
				}
				// Update own index in the new set
				newIdx := newSet.FindByAddress(myAddr)
				if newIdx >= 0 {
					e.myIndex = ValidatorIndex(newIdx)
				} else {
					log.Warn("This node removed from validator set at epoch boundary",
						"address", myAddr.Hex(), "epoch", e.epochManager.CurrentEpoch()+1)
					_ = e.emit(EngineOutput{
						Type:    OutputEpochTransition,
						Removed: true,
					})
					e.removed = true
					e.pacemaker.Stop()
					return nil // stop consensus participation immediately
				}
			}
		}

		if e.epochManager.AdvanceEpoch() {
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
	e.importedBlocks = make(map[types.Hash]bool)
	e.pendingTxRoots = make(map[types.Hash]types.Hash)
	e.equivocationTracker = make(map[ValidatorIndex]types.Hash)
	e.commitEquivocationTracker = make(map[ValidatorIndex]types.Hash)
	e.prepareVoteBuf = e.prepareVoteBuf[:0]
	e.commitVoteBuf = e.commitVoteBuf[:0]

	// Preserve timing from committed view.
	if e.viewTiming.CommitQCFormed != nil {
		timing := e.viewTiming
		e.lastCommittedTiming = &timing
	}
	e.viewTiming = newViewTiming()

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

func (e *ConsensusEngine) tryQCViewJump(msg *ConsensusMsg, msgView ViewNumber) (bool, error) {
	currentView := e.roundState.CurrentView()

	qc := extractQCFromMessage(msg)
	if qc == nil || qc.View == 0 {
		return false, nil
	}

	// Verify QC. Decide uses commit signing domain.
	var verifyErr error
	if msg.Type == MsgDecide {
		verifyErr = VerifyCommitQC(qc, e.validatorSet())
	} else {
		verifyErr = VerifyQC(qc, e.validatorSet())
	}
	if verifyErr != nil {
		return false, nil
	}

	targetView := qc.View + 1
	if msgView > targetView {
		targetView = msgView
	}
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
	return VerifyQCAnyDomain(qc, e.validatorSet())
}
