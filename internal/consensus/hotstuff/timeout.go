// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// onTimeout handles a view timeout triggered by the pacemaker.
func (e *ConsensusEngine) onTimeout() error {
	view := e.roundState.CurrentView()

	if e.roundState.Phase() == PhaseTimedOut {
		// Already timed out: re-broadcast timeout for late arrivals.
		log.Warn("view timed out (repeat, re-broadcasting)", "view", view)
		e.pacemaker.ResetForView(view, e.roundState.ConsecutiveTimeouts())

		message := TimeoutSigningMessage(view)
		signature := e.secretKey.Sign(message)
		timeoutMsg := &TimeoutMessage{
			View:      view,
			HighQC:    e.roundState.LockedQC().Clone(),
			Sender:    e.myIndex,
			Signature: signature.Marshal(),
		}
		if err := e.emit(EngineOutput{
			Type: OutputBroadcast,
			Message: &ConsensusMsg{
				Type:    MsgTimeout,
				Payload: timeoutMsg,
			},
		}); err != nil {
			return err
		}

		// Check if quorum was reached while waiting.
		quorumSize := e.validatorSet().QuorumSize()
		nextView := view + 1
		nextLeader := LeaderForView(nextView, e.validatorSet())
		if e.timeoutCollector != nil && e.timeoutCollector.HasQuorum(quorumSize) && nextLeader == e.myIndex {
			return e.tryFormTCAndAdvance(view, nextView)
		}
		return nil
	}

	log.Warn("view timed out", "view", view)

	// Flush any pending vote buffers before transitioning.
	if len(e.prepareVoteBuf) > 0 {
		_ = e.flushPrepareVotes()
	}
	if len(e.commitVoteBuf) > 0 {
		_ = e.flushCommitVotes()
	}

	e.roundState.Timeout()
	e.pacemaker.ResetForView(view, e.roundState.ConsecutiveTimeouts())

	// Clear pending block data.
	e.importedBlocks = make(map[types.Hash]bool)
	e.pendingTxRoots = make(map[types.Hash]types.Hash)

	// Preserve any already-collected timeouts.
	nValidators := e.validatorSet().Len()
	if e.timeoutCollector == nil {
		e.timeoutCollector = NewTimeoutCollector(view, nValidators)
	}

	message := TimeoutSigningMessage(view)
	signature := e.secretKey.Sign(message)

	timeoutMsg := &TimeoutMessage{
		View:      view,
		HighQC:    e.roundState.LockedQC().Clone(),
		Sender:    e.myIndex,
		Signature: signature.Marshal(),
	}

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgTimeout,
			Payload: timeoutMsg,
		},
	}); err != nil {
		return err
	}

	return e.processTimeout(timeoutMsg)
}

// processTimeout processes a timeout message.
func (e *ConsensusEngine) processTimeout(timeout *TimeoutMessage) error {
	view := e.roundState.CurrentView()

	if timeout.View < view {
		return nil
	}

	if timeout.View > view {
		return e.handleFutureViewTimeout(view, timeout)
	}

	// Current-view timeout processing.
	pk, err := e.validatorSet().GetPublicKey(timeout.Sender)
	if err != nil {
		return err
	}
	msg := TimeoutSigningMessage(view)
	if !VerifyBLSSignature(timeout.Signature, pk, msg) {
		return &InvalidSignatureError{View: view, ValidatorIndex: timeout.Sender}
	}

	// Verify embedded high_qc.
	if err := e.verifyEmbeddedQC(&timeout.HighQC); err != nil {
		log.Warn("rejecting timeout with invalid high_qc",
			"view", view, "sender", timeout.Sender, "err", err)
		return err
	}

	nValidators := e.validatorSet().Len()
	quorumSize := e.validatorSet().QuorumSize()
	nextView := view + 1
	nextLeader := LeaderForView(nextView, e.validatorSet())

	if e.timeoutCollector == nil {
		e.timeoutCollector = NewTimeoutCollector(view, nValidators)
	}

	sig, sErr := bls.SignatureFromBytes(timeout.Signature)
	if sErr != nil {
		return &InvalidSignatureError{View: view, ValidatorIndex: timeout.Sender}
	}

	if err := e.timeoutCollector.AddVerifiedTimeout(timeout.Sender, sig, timeout.HighQC.Clone()); err != nil {
		if _, ok := err.(*DuplicateVoteError); ok {
			return nil
		}
		return err
	}

	if e.timeoutCollector.HasQuorum(quorumSize) && nextLeader == e.myIndex {
		return e.tryFormTCAndAdvance(view, nextView)
	}

	return nil
}

// handleFutureViewTimeout handles a timeout from a future view.
func (e *ConsensusEngine) handleFutureViewTimeout(currentView ViewNumber, timeout *TimeoutMessage) error {
	if timeout.View > currentView+FutureViewWindow {
		return nil
	}

	// Verify BLS signature BEFORE advancing.
	pk, err := e.validatorSet().GetPublicKey(timeout.Sender)
	if err != nil {
		return err
	}
	msg := TimeoutSigningMessage(timeout.View)
	if !VerifyBLSSignature(timeout.Signature, pk, msg) {
		return &InvalidSignatureError{View: timeout.View, ValidatorIndex: timeout.Sender}
	}

	// Verify embedded high_qc.
	if err := e.verifyEmbeddedQC(&timeout.HighQC); err != nil {
		return err
	}

	log.Info("advancing to higher timeout view for synchronization",
		"currentView", currentView, "timeoutView", timeout.View, "sender", timeout.Sender)

	// Mark timeout BEFORE advancing so that advanceToView replays buffered
	// messages with the correct phase (PhaseTimedOut) and backoff level.
	e.roundState.Timeout()
	if err := e.advanceToView(timeout.View); err != nil {
		return err
	}

	// Create timeout collector if needed.
	nValidators := e.validatorSet().Len()
	if e.timeoutCollector == nil || e.timeoutCollector.View() != timeout.View {
		e.timeoutCollector = NewTimeoutCollector(timeout.View, nValidators)
	}

	sig, sErr := bls.SignatureFromBytes(timeout.Signature)
	if sErr == nil {
		if err := e.timeoutCollector.AddVerifiedTimeout(timeout.Sender, sig, timeout.HighQC.Clone()); err != nil {
			if _, ok := err.(*DuplicateVoteError); !ok {
				return err
			}
		}
	}

	// Broadcast our own timeout.
	ownMsg := TimeoutSigningMessage(timeout.View)
	ownSig := e.secretKey.Sign(ownMsg)
	ownTimeout := &TimeoutMessage{
		View:      timeout.View,
		HighQC:    e.roundState.LockedQC().Clone(),
		Sender:    e.myIndex,
		Signature: ownSig.Marshal(),
	}

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgTimeout,
			Payload: ownTimeout,
		},
	}); err != nil {
		return err
	}

	return e.processTimeout(ownTimeout)
}

// processNewView processes a NewView message from the new leader.
func (e *ConsensusEngine) processNewView(nv *NewViewMsg) error {
	view := e.roundState.CurrentView()

	if nv.View == 0 || nv.View <= view {
		return nil
	}

	expectedLeader := LeaderForView(nv.View, e.validatorSet())
	if nv.Leader != expectedLeader {
		return &InvalidProposerError{View: nv.View, Expected: expectedLeader, Actual: nv.Leader}
	}

	// Verify leader's signature.
	pk, err := e.validatorSet().GetPublicKey(nv.Leader)
	if err != nil {
		return err
	}
	nvMsg := NewViewSigningMessage(nv.View)
	if !VerifyBLSSignature(nv.Signature, pk, nvMsg) {
		return &InvalidSignatureError{View: nv.View, ValidatorIndex: nv.Leader}
	}

	// TC must be for the previous view.
	if nv.TimeoutCert.View != nv.View-1 {
		return &InvalidTCError{
			View:   nv.TimeoutCert.View,
			Reason: "TC view does not match expected view (nv.view - 1)",
		}
	}
	if err := VerifyTC(&nv.TimeoutCert, e.validatorSet()); err != nil {
		return err
	}

	// Verify TC's high_qc.
	if err := e.verifyEmbeddedQC(&nv.TimeoutCert.HighQC); err != nil {
		return err
	}

	e.roundState.UpdateLockedQC(&nv.TimeoutCert.HighQC)

	log.Info("received NewView, advancing", "oldView", view, "newView", nv.View)

	if err := e.advanceToView(nv.View); err != nil {
		return err
	}
	actualView := e.roundState.CurrentView()
	return e.emit(EngineOutput{Type: OutputViewChanged, View: actualView})
}

// processDecide processes a Decide message from the leader.
func (e *ConsensusEngine) processDecide(decide *Decide) error {
	currentView := e.roundState.CurrentView()

	if decide.View < currentView {
		return nil
	}

	quorumSize := e.validatorSet().QuorumSize()
	if decide.CommitQC.SignerCount() < quorumSize {
		return &InsufficientVotesError{
			View: decide.View,
			Have: decide.CommitQC.SignerCount(),
			Need: quorumSize,
		}
	}

	if decide.CommitQC.View != decide.View {
		return &ViewMismatchError{Current: decide.View, Received: decide.CommitQC.View}
	}

	if decide.CommitQC.BlockHash != decide.BlockHash {
		return &BlockHashMismatchError{Expected: decide.BlockHash, Got: decide.CommitQC.BlockHash}
	}

	// Verify CommitQC aggregate BLS signature.
	if err := VerifyCommitQC(&decide.CommitQC, e.validatorSet()); err != nil {
		return err
	}

	if decide.View > currentView+SyncGapThreshold {
		log.Warn("large view gap detected, requesting state sync",
			"currentView", currentView, "decideView", decide.View)
		if err := e.emit(EngineOutput{
			Type:       OutputSyncRequired,
			LocalView:  currentView,
			TargetView: decide.View,
		}); err != nil {
			return err
		}
	}

	log.Info("received Decide, committing block", "view", decide.View, "blockHash", decide.BlockHash)

	now := time.Now()
	e.viewTiming.CommitQCFormed = &now

	e.roundState.UpdateLockedQC(&decide.CommitQC)
	e.roundState.Commit(decide.CommitQC.Clone())

	if err := e.emit(EngineOutput{
		Type: OutputBlockCommitted,
		View: decide.View,
		Hash: decide.BlockHash,
		QC:   &decide.CommitQC,
	}); err != nil {
		return err
	}

	nextView := decide.View + 1
	if err := e.advanceToView(nextView); err != nil {
		return err
	}

	actualView := e.roundState.CurrentView()
	return e.emit(EngineOutput{Type: OutputViewChanged, View: actualView})
}

// tryFormTCAndAdvance builds a TC from the current timeout_collector and broadcasts NewView.
func (e *ConsensusEngine) tryFormTCAndAdvance(currentView, nextView ViewNumber) error {
	if e.timeoutCollector == nil {
		return nil
	}

	tc, err := e.timeoutCollector.BuildTC(e.validatorSet())
	if err != nil {
		return err
	}

	log.Info("TC formed, I am the new leader", "view", currentView, "nextView", nextView)

	e.roundState.UpdateLockedQC(&tc.HighQC)

	nvMessage := NewViewSigningMessage(nextView)
	nvSig := e.secretKey.Sign(nvMessage)

	newView := &NewViewMsg{
		View:        nextView,
		TimeoutCert: *tc,
		Leader:      e.myIndex,
		Signature:   nvSig.Marshal(),
	}

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgNewView,
			Payload: newView,
		},
	}); err != nil {
		return err
	}

	if err := e.advanceToView(nextView); err != nil {
		return err
	}
	actualView := e.roundState.CurrentView()
	return e.emit(EngineOutput{Type: OutputViewChanged, View: actualView})
}
