// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto/bls/common"
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

	epochManager *EpochManager
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
	importedBlocks     map[types.Hash]bool
	equivocationTracker map[ValidatorIndex]types.Hash

	// Future message buffer
	futureMsgBuffer []futureMsg

	// Timing
	viewTiming         ViewTiming
	lastCommittedTiming *ViewTiming
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
	return &ConsensusEngine{
		myIndex:             myIndex,
		secretKey:           secretKey,
		epochManager:        epochManager,
		roundState:          NewRoundState(),
		pacemaker:           NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:            outputCh,
		importedBlocks:      make(map[types.Hash]bool),
		equivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:     make([]futureMsg, 0),
		viewTiming:          newViewTiming(),
	}
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

	return &ConsensusEngine{
		myIndex:             myIndex,
		secretKey:           secretKey,
		epochManager:        epochManager,
		roundState:          RoundStateFromSnapshot(recoveredView, lockedQC, lastCommittedQC, consecutiveTimeouts),
		pacemaker:           NewPacemaker(baseTimeoutMs, maxTimeoutMs),
		outputCh:            outputCh,
		importedBlocks:      make(map[types.Hash]bool),
		equivocationTracker: make(map[ValidatorIndex]types.Hash),
		futureMsgBuffer:     make([]futureMsg, 0),
		viewTiming:          newViewTiming(),
	}
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

	switch event.Type {
	case EventMessage:
		return e.processMessage(event.Msg)
	case EventBlockReady:
		return e.onBlockReady(event.Hash)
	case EventBlockImported:
		return e.onBlockImported(event.Hash)
	default:
		return nil
	}
}

// OnTimeout handles a view timeout triggered by the pacemaker (thread-safe).
func (e *ConsensusEngine) OnTimeout() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.onTimeout()
}

// ConsensusEvent represents events fed into the consensus engine.
type ConsensusEvent struct {
	Type ConsensusEventType
	Msg  ConsensusMsg
	Hash types.Hash
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

func (e *ConsensusEngine) emit(output EngineOutput) error {
	select {
	case e.outputCh <- output:
		return nil
	default:
		// For BlockCommitted events, retry with brief sleeps.
		if output.Type == OutputBlockCommitted {
			for attempt := 1; attempt <= 3; attempt++ {
				time.Sleep(time.Millisecond)
				select {
				case e.outputCh <- output:
					log.Warn("BlockCommitted delivered after retry", "attempt", attempt)
					return nil
				default:
				}
			}
			log.Error("CRITICAL: BlockCommitted lost after 3 retries")
		}
		return ErrOutputChannelClosed
	}
}

func (e *ConsensusEngine) advanceToView(newView ViewNumber) error {
	if newView <= e.roundState.CurrentView() {
		return nil
	}

	// Save current PrepareQC for piggybacking.
	e.previousPrepareQC = e.prepareQC

	// Check epoch boundary.
	if e.epochManager.EpochsEnabled() &&
		e.epochManager.IsEpochBoundary(newView) &&
		e.epochManager.AdvanceEpoch() {
		newEpoch := e.epochManager.CurrentEpoch()
		validatorCount := e.validatorSet().Len()
		log.Info("epoch transition at view boundary", "epoch", newEpoch, "validators", validatorCount, "view", newView)
		if err := e.emit(EngineOutput{
			Type:           OutputEpochTransition,
			NewEpoch:       newEpoch,
			ValidatorCount: validatorCount,
		}); err != nil {
			return err
		}
	}

	e.roundState.AdvanceView(newView)
	e.pacemaker.ResetForView(newView, e.roundState.ConsecutiveTimeouts())
	e.voteCollector = nil
	e.commitCollector = nil
	e.timeoutCollector = nil
	e.prepareQC = nil
	e.importedBlocks = make(map[types.Hash]bool)
	e.equivocationTracker = make(map[ValidatorIndex]types.Hash)

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
	switch msg.Type {
	case MsgProposal:
		return e.processProposal(msg.Payload.(*Proposal))
	case MsgVote:
		return e.processVote(msg.Payload.(*Vote))
	case MsgCommitVote:
		return e.processCommitVote(msg.Payload.(*CommitVote))
	case MsgPrepareQC:
		return e.processPrepareQC(msg.Payload.(*PrepareQCMsg))
	case MsgTimeout:
		return e.processTimeout(msg.Payload.(*TimeoutMessage))
	case MsgNewView:
		return e.processNewView(msg.Payload.(*NewViewMsg))
	case MsgDecide:
		return e.processDecide(msg.Payload.(*Decide))
	}
	return nil
}

func messageView(msg ConsensusMsg) ViewNumber {
	switch msg.Type {
	case MsgProposal:
		return msg.Payload.(*Proposal).View
	case MsgVote:
		return msg.Payload.(*Vote).View
	case MsgCommitVote:
		return msg.Payload.(*CommitVote).View
	case MsgPrepareQC:
		return msg.Payload.(*PrepareQCMsg).View
	case MsgTimeout:
		return msg.Payload.(*TimeoutMessage).View
	case MsgNewView:
		return msg.Payload.(*NewViewMsg).View
	case MsgDecide:
		return msg.Payload.(*Decide).View
	}
	return 0
}

func extractQCFromMessage(msg *ConsensusMsg) *QuorumCertificate {
	switch msg.Type {
	case MsgProposal:
		qc := msg.Payload.(*Proposal).JustifyQC
		return &qc
	case MsgTimeout:
		qc := msg.Payload.(*TimeoutMessage).HighQC
		return &qc
	case MsgDecide:
		qc := msg.Payload.(*Decide).CommitQC
		return &qc
	case MsgPrepareQC:
		qc := msg.Payload.(*PrepareQCMsg).QC
		return &qc
	case MsgNewView:
		qc := msg.Payload.(*NewViewMsg).TimeoutCert.HighQC
		return &qc
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
