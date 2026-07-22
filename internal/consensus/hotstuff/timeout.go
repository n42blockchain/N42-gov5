// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// View timeout handling for the HotStuff-2 engine.
// onTimeout is invoked by the pacemaker when the current view
// exceeds its deadline. It transitions the round state to
// PhaseTimedOut, broadcasts a TimeoutMsg signed with the local BLS
// key and drives view change recovery without breaking safety locks.

package hotstuff

import (
	"sort"
	"time"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/log"
)

// onTimeout handles a view timeout triggered by the pacemaker.
func (e *ConsensusEngine) onTimeout() error {
	view := e.roundState.CurrentView()

	// Guard against a stale timer fire. The pacemaker reuses one timer that the
	// loop re-arms only after it fires; ResetForView (called on every view
	// advance) moves the deadline forward but does NOT re-arm the timer. So a
	// timer armed for an earlier view fires ~base-timeout after that view began,
	// by which point many healthy views may have committed — declaring the
	// CURRENT view timed out here would be a spurious timeout every base-timeout
	// interval, churning a needless view change. Ignore a fire whose deadline
	// has not actually passed; the loop re-arms to the current deadline on its
	// next iteration, so a genuine timeout still fires at the correct time.
	if !e.pacemaker.IsTimedOut() {
		return nil
	}

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
			HighTC:    e.roundState.HighestTC(),
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

		// Re-send this view's vote alongside the timeout re-broadcast: votes are
		// single-shot; on a lossy/degraded mesh the leader may have missed the
		// vote even though this node cast it (observed live: all nodes logged
		// "voting now" yet no QC formed). The leader's collector treats a
		// duplicate vote as a no-op, so the re-send is harmless when the first
		// one did arrive.
		if h, ok := e.roundState.VotedHashInView(view); ok {
			if verr := e.sendVote(view, h); verr != nil {
				log.Debug("timeout vote re-send failed", "view", view, "err", verr)
			}
		}

		// Check if quorum was reached while waiting. ANY node holding a
		// timeout quorum forms the TC and advances — view progression must
		// not depend on the next leader being alive (see tryFormTCAndAdvance).
		quorumSize := e.validatorSet().QuorumSize()
		nextView := view + 1
		if e.timeoutCollector != nil && e.timeoutCollector.HasQuorum(quorumSize) {
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

	// Keep importedBlocks/pendingTxRoots across the timeout: a block we imported
	// we still have, and the new-view leader re-proposes it. Wiping here caused
	// the same import-gated-vote deadlock as wiping on advance (see advanceToView).

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
		HighTC:    e.roundState.HighestTC(),
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

	// ANY node holding a timeout quorum forms the TC and advances — view
	// progression must not depend on the next leader being alive (see
	// tryFormTCAndAdvance).
	if e.timeoutCollector.HasQuorum(quorumSize) {
		return e.tryFormTCAndAdvance(view, nextView)
	}

	return nil
}

// handleFutureViewTimeout handles a timeout from a future view.
//
// A single raw future timeout is not enough to advance: one Byzantine
// validator could otherwise drag the fleet forward indefinitely (H2). A TC is
// the strong synchronizer and is adopted by processEmbeddedTC before this
// handler. There is also a necessary weak-synchronizer path for crash recovery:
// nodes can legitimately restart at scattered persisted views before any one
// view has a TC. Once f+1 distinct validators have sent valid future timeouts,
// at least one report is honest. We catch up to the (f+1)-th highest reported
// view within the bounded FutureViewWindow and broadcast our own timeout there.
// This target has f+1 reports at or above it, while the f Byzantine validators
// cannot either trigger the jump alone or use one extreme report to select an
// otherwise-unbacked target.
func (e *ConsensusEngine) handleFutureViewTimeout(currentView ViewNumber, timeout *TimeoutMessage) error {
	if timeout.View > currentView+FutureViewWindow {
		return nil
	}

	// Verify BLS signature so a forged timeout can't pollute anything downstream.
	pk, err := e.validatorSet().GetPublicKey(timeout.Sender)
	if err != nil {
		return err
	}
	msg := TimeoutSigningMessage(timeout.View)
	if !VerifyBLSSignature(timeout.Signature, pk, msg) {
		return &InvalidSignatureError{View: timeout.View, ValidatorIndex: timeout.Sender}
	}

	// Its TC, if any, was already adopted by processEmbeddedTC upstream.
	if err := e.verifyEmbeddedQC(&timeout.HighQC); err != nil {
		return err
	}

	if e.futureTimeouts == nil {
		e.futureTimeouts = make(map[ValidatorIndex]*TimeoutMessage)
	}
	if previous := e.futureTimeouts[timeout.Sender]; previous == nil || timeout.View > previous.View {
		e.futureTimeouts[timeout.Sender] = &TimeoutMessage{
			View:      timeout.View,
			HighQC:    timeout.HighQC.Clone(),
			Sender:    timeout.Sender,
			Signature: append([]byte(nil), timeout.Signature...),
		}
	}

	threshold := int(e.validatorSet().FaultTolerance()) + 1
	views := make([]ViewNumber, 0, len(e.futureTimeouts))
	for sender, observed := range e.futureTimeouts {
		if observed.View <= currentView {
			delete(e.futureTimeouts, sender)
			continue
		}
		views = append(views, observed.View)
	}
	if len(views) < threshold {
		log.Debug("future-view timeout observed; waiting for f+1 distinct validators",
			"currentView", currentView, "timeoutView", timeout.View, "sender", timeout.Sender,
			"observed", len(views), "required", threshold)
		return nil
	}

	sort.Slice(views, func(i, j int) bool { return views[i] > views[j] })
	targetView := views[threshold-1]
	if targetView <= currentView {
		return nil
	}

	// Keep already-verified reports exactly at the selected target so they
	// can seed its collector after advanceToView clears old observations.
	var targetTimeouts []*TimeoutMessage
	for _, observed := range e.futureTimeouts {
		if observed.View == targetView {
			targetTimeouts = append(targetTimeouts, observed)
		}
	}

	log.Warn("f+1 future timeouts observed; advancing weak synchronizer",
		"currentView", currentView, "targetView", targetView,
		"observed", len(views), "required", threshold, "targetRank", threshold)
	if err := e.advanceToView(targetView); err != nil {
		return err
	}
	if e.roundState.CurrentView() != targetView || e.removed {
		return nil
	}

	// This is a timeout-driven catch-up, not a proposal view: enter timed-out
	// phase immediately and help the target view form its strong TC.
	e.roundState.Timeout()
	e.pacemaker.ResetForView(targetView, e.roundState.ConsecutiveTimeouts())
	e.timeoutCollector = NewTimeoutCollector(targetView, e.validatorSet().Len())

	for _, observed := range targetTimeouts {
		// A retained report can fail re-verification if advanceToView crossed an
		// epoch boundary (the validator set changed, so the old-set signature no
		// longer verifies). That is not a reason to abort recovery: skip the
		// stale seed and still broadcast our own timeout below so the new-set TC
		// can form. Any other processTimeout outcome (added to collector, or it
		// formed a TC and moved the view) is handled by the view re-check.
		if err := e.processTimeout(observed); err != nil {
			log.Debug("hotstuff: skipping unverifiable retained timeout seed during recovery",
				"targetView", targetView, "sender", observed.Sender, "err", err)
			continue
		}
		if e.roundState.CurrentView() != targetView {
			return nil // the retained reports already formed a TC
		}
	}

	ownSignature := e.secretKey.Sign(TimeoutSigningMessage(targetView))
	ownTimeout := &TimeoutMessage{
		View:      targetView,
		HighQC:    e.roundState.LockedQC().Clone(),
		Sender:    e.myIndex,
		Signature: ownSignature.Marshal(),
		HighTC:    e.roundState.HighestTC(),
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
	if err := VerifyTC(&nv.TimeoutCert, e.resolveQCValidatorSet(nv.TimeoutCert.View, len(nv.TimeoutCert.Signers))); err != nil {
		return err
	}

	// Verify TC's high_qc.
	if err := e.verifyEmbeddedQC(&nv.TimeoutCert.HighQC); err != nil {
		return err
	}

	e.roundState.UpdateLockedQC(&nv.TimeoutCert.HighQC)
	e.roundState.UpdateHighestTC(&nv.TimeoutCert)

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
	if err := VerifyCommitQC(&decide.CommitQC, e.resolveQCValidatorSet(decide.CommitQC.View, len(decide.CommitQC.Signers))); err != nil {
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

	e.roundState.UpdateLockedQC(&tc.HighQC)
	e.roundState.UpdateHighestTC(tc)

	// Only the legitimate next-view leader announces the NewView; every other
	// node advances LOCALLY on its own quorum and the TC travels via the
	// SyncInfo piggyback (HighTC on subsequent votes/timeouts). View
	// progression must never depend on the next leader being alive: with the
	// mesh at exactly quorum size and the next leader among the dead, the old
	// leader-only rule left every survivor holding a complete TC it was not
	// allowed to act on — the whole network spun on a single view
	// re-broadcasting timeouts forever (observed live: 5/7 nodes wedged on
	// view 5408, +0 blocks in 4 minutes, timeout counters climbing).
	if LeaderForView(nextView, e.validatorSet()) == e.myIndex {
		log.Info("TC formed, I am the new leader", "view", currentView, "nextView", nextView)

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
	} else {
		log.Info("TC formed locally, advancing without the next leader",
			"view", currentView, "nextView", nextView)
	}

	if err := e.advanceToView(nextView); err != nil {
		return err
	}
	actualView := e.roundState.CurrentView()
	return e.emit(EngineOutput{Type: OutputViewChanged, View: actualView})
}
