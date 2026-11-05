// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Deferred-vote retry (N42_VOTE_DEFER_RETRY=1). The committed floor
// (committed_floor.go) answers "wait" when this node cannot resolve a
// block's ancestry down to its committed block yet -- typically a light
// follower that has not stored or linked the parent. The Round 2 vote sites
// used to treat that exactly like a refusal and drop the commit vote, with
// nothing to bring it back: run 36 B2 block 14129615 lost its CommitQC when
// three of seven nodes did so, the view timed out after 6 s and a sibling
// was built. With the switch on, a "wait" commit vote is held instead and
// re-evaluated when the ancestry can have changed (a block's header known,
// checked or imported, this node's committed head advancing) plus a short
// bounded timer. A held vote is released only through the very function
// that would have sent it immediately, so it passes every check an
// immediate vote passes; a "refuse" verdict is never held.

package hotstuff

import (
	"os"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// voteDeferRetry is N42_VOTE_DEFER_RETRY=1. Off by default: the vote sites
// then drop a "wait" vote exactly as before.
var voteDeferRetry = os.Getenv("N42_VOTE_DEFER_RETRY") == "1"

// SetVoteDeferRetryForTest switches voteDeferRetry for engines constructed
// afterwards and returns the restore.
func SetVoteDeferRetryForTest(on bool) func() {
	prev := voteDeferRetry
	voteDeferRetry = on
	return func() { voteDeferRetry = prev }
}

const (
	// floorRetryInterval is the fallback re-check period while a vote is held.
	floorRetryInterval = 20 * time.Millisecond
	// floorRetryMaxAttempts bounds the fallback timer to about 6 s, the base
	// view timeout: a light node's import lag drains in 1-3 s (run 36), and
	// every import event re-checks a held vote on top of the timer.
	floorRetryMaxAttempts = 300
)

// floorHeldCommit is a Round 2 commit vote whose extends check returned
// "wait". journaled: the commitment is already durable (commitJournalEarly
// held path), so the release goes through releaseJournaledCommitVote.
type floorHeldCommit struct {
	pqc       PrepareQCMsg
	journaled bool
}

// extendsJustifyVerdict is extendsJustify with the unknown case kept apart:
// floorAllow (extends), floorRefuse (provably does not extend its JustifyQC
// block or the committed block) or floorWait (the committed-floor ancestry
// is not known yet). Caller must hold e.mu.
func (e *ConsensusEngine) extendsJustifyVerdict(view ViewNumber, blockHash types.Hash) floorVerdict {
	if v := e.committedVerdict(view, blockHash); v != floorAllow {
		return v
	}
	if !e.justifyMatches(view, blockHash) {
		return floorRefuse
	}
	return floorAllow
}

// holdFloorWaitCommit parks a commit vote whose extends verdict is "wait"
// for view r2View(). Reports whether it was held (switch on, member).
func (e *ConsensusEngine) holdFloorWaitCommit(pqc *PrepareQCMsg, journaled bool) bool {
	if !e.deferRetry || !e.isMember() {
		return false
	}
	e.floorHeld = &floorHeldCommit{pqc: *pqc, journaled: journaled}
	e.floorRetryAttempts = 0
	metricVoteDeferHeld.Inc()
	log.Info("hotstuff commit vote held: ancestry to the committed block not known yet, re-checking",
		"view", pqc.View, "blockHash", pqc.BlockHash, "journaled", journaled)
	e.armFloorRetry()
	return true
}

// notePrepareFloorWait arms the fallback re-check for a Round 1 vote whose
// extends verdict was "wait". The Round 1 paths re-evaluate on every block
// event already; this only adds the timer and committed-head triggers.
func (e *ConsensusEngine) notePrepareFloorWait(view ViewNumber) {
	if !e.deferRetry || !e.isMember() {
		return
	}
	if !e.prepareFloorWait || e.prepareFloorWaitView != view {
		e.floorRetryAttempts = 0
	}
	e.prepareFloorWait, e.prepareFloorWaitView = true, view
	e.armFloorRetry()
}

// prepareExtends is extendsJustify for the Round 1 vote sites: a "wait"
// verdict arms the deferred-vote re-check when the switch is on.
func (e *ConsensusEngine) prepareExtends(view ViewNumber, blockHash types.Hash) bool {
	v := e.extendsJustifyVerdict(view, blockHash)
	if v == floorWait {
		e.notePrepareFloorWait(view)
	}
	return v == floorAllow
}

// armFloorRetry schedules one fallback re-check unless one is pending or
// the attempt budget is spent. Caller must hold e.mu.
func (e *ConsensusEngine) armFloorRetry() {
	if e.floorRetryArmed || e.floorRetryAttempts >= floorRetryMaxAttempts {
		return
	}
	e.floorRetryArmed = true
	e.floorRetryAttempts++
	time.AfterFunc(floorRetryInterval, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.floorRetryArmed = false
		e.retryDeferredVotes("timer")
		if e.floorHeld != nil || e.prepareFloorWait {
			e.armFloorRetry()
		}
	})
}

// RecheckDeferredVotes re-evaluates held votes after this node's committed
// head advanced (the service calls it once CommitToCanonical lands).
// Thread-safe; a no-op unless N42_VOTE_DEFER_RETRY=1.
func (e *ConsensusEngine) RecheckDeferredVotes() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retryDeferredVotes("committed")
}

// dropFloorHeldOnViewChange drops held votes for views left behind by the
// move to newView. A commit vote held for the view that pipelineAdvance just
// turned into the trailing round survives: its Round 2 is still open.
// Caller must hold e.mu.
func (e *ConsensusEngine) dropFloorHeldOnViewChange(newView ViewNumber) {
	if h := e.floorHeld; h != nil && h.pqc.View < newView &&
		(e.trailing == nil || e.trailing.view != h.pqc.View) {
		e.floorHeld = nil
		log.Info("hotstuff held commit vote dropped: view changed",
			"view", h.pqc.View, "blockHash", h.pqc.BlockHash, "newView", newView)
	}
	if e.prepareFloorWait && e.prepareFloorWaitView < newView {
		e.prepareFloorWait = false
	}
}

// retryDeferredVotes re-evaluates the held votes. Caller must hold e.mu.
func (e *ConsensusEngine) retryDeferredVotes(trigger string) {
	if !e.deferRetry || e.trailingActive {
		return
	}
	e.retryHeldCommit(trigger)
	e.retryPrepareFloorWait(trigger)
}

// retryHeldCommit releases the held commit vote once its ancestry resolves
// to the committed block, drops it once it provably does not, and keeps it
// otherwise. The release re-enters processPrepareQC (or, for a journaled
// commitment, releaseJournaledCommitVote) for the held view, in the trailing
// round when that view is trailing: the view, double-vote, QC, attestation,
// lock and full extends checks all run again exactly as for an immediate
// vote, and a vote still undecided there is simply held again.
func (e *ConsensusEngine) retryHeldCommit(trigger string) {
	h := e.floorHeld
	if h == nil {
		return
	}
	view := h.pqc.View
	trailing := e.trailing != nil && e.trailing.view == view
	if !trailing && view != e.roundState.CurrentView() {
		e.floorHeld = nil
		log.Info("hotstuff held commit vote dropped: view changed",
			"view", view, "blockHash", h.pqc.BlockHash, "currentView", e.roundState.CurrentView())
		return
	}
	switch e.checkCommittedFloor(h.pqc.BlockHash) {
	case floorWait:
		return
	case floorRefuse:
		e.floorHeld = nil
		cNum, cHash, _, _ := e.committedChain.CommittedHead()
		log.Warn("hotstuff held commit vote dropped: block does not extend this node's committed block",
			"view", view, "blockHash", h.pqc.BlockHash, "committedNumber", cNum, "committedHash", cHash)
		metricVoteCommittedFloor.Inc()
		return
	}
	e.floorHeld = nil
	metricVoteDeferReleased.Inc()
	log.Info("hotstuff held commit vote: ancestry resolved, re-evaluating",
		"view", view, "blockHash", h.pqc.BlockHash, "trigger", trigger, "trailing", trailing)
	pqc := h.pqc
	release := func() error {
		if h.journaled {
			return e.releaseJournaledCommitVote(&pqc)
		}
		return e.processPrepareQC(&pqc, msgTiming{})
	}
	var err error
	if trailing {
		err = e.inTrailing(release)
	} else {
		err = release()
	}
	if err != nil {
		log.Debug("hotstuff held commit vote release failed", "view", view, "err", err)
	}
}

// retryPrepareFloorWait re-runs the Round 1 vote paths for the current view
// after a "wait" verdict. They carry all their own guards (pending proposal,
// one vote per view, header/check/import gates, extends check).
func (e *ConsensusEngine) retryPrepareFloorWait(trigger string) {
	if !e.prepareFloorWait {
		return
	}
	view := e.roundState.CurrentView()
	pending, ok := e.pendingProposals[view]
	if e.prepareFloorWaitView != view || !ok || e.roundState.HasVotedInView(view) {
		e.prepareFloorWait = false
		return
	}
	switch e.checkCommittedFloor(pending) {
	case floorWait:
		return
	case floorRefuse:
		e.prepareFloorWait = false
		return
	}
	e.prepareFloorWait = false // a fresh "wait" below re-arms it
	if voted, err := e.tryHeaderVote(view); voted || err != nil {
		return
	}
	if voted, err := e.tryDeferredVote(view); voted || err != nil {
		return
	}
	if _, err := e.tryImportedVote(view); err != nil {
		log.Debug("hotstuff deferred prepare vote failed", "view", view, "trigger", trigger, "err", err)
	}
}

// tryImportedVote is onBlockImported's import-gated Round 1 vote for a
// pending proposal this node has already imported.
func (e *ConsensusEngine) tryImportedVote(view ViewNumber) (bool, error) {
	pending, ok := e.pendingProposals[view]
	if !ok || !e.importedBlocks[pending] || e.roundState.HasVotedInView(view) {
		return false, nil
	}
	if !e.prepareExtends(view, pending) {
		return false, nil
	}
	if err := e.journalPrepareVote(view, pending); err != nil {
		return false, err
	}
	log.Info("import-gated vote: casting deferred vote after ancestry resolved", "view", view, "blockHash", pending)
	return true, e.sendVote(view, pending)
}

// floorLagHeaders is N42_FLOOR_LAG_HEADERS=1: keep parent links for the
// committed-floor walk in a window of floorParentWindow blocks instead of
// importedParents' MaxImportedBlocks. A light follower's committed marker
// trails its import, and blocks pushed but not yet imported have no stored
// header, so once the import lag passes ~64 blocks the walk loses the chain
// and every vote waits (run 36: deferrals start at lag 63). Off by default.
//
// Safety: the links come from exactly the events that fill importedParents
// (header known, checked, imported), each carrying a parent read from the
// block header whose hash IS the key, so a link cannot be forged without a
// hash collision. Only their retention changes. The walk still allows a
// block only when it reaches this node's committed block through such
// links, and still refuses or waits exactly as before otherwise.
var floorLagHeaders = os.Getenv("N42_FLOOR_LAG_HEADERS") == "1"

// SetFloorLagHeadersForTest switches floorLagHeaders for engines constructed
// afterwards and returns the restore.
func SetFloorLagHeadersForTest(on bool) func() {
	prev := floorLagHeaders
	floorLagHeaders = on
	return func() { floorLagHeaders = prev }
}

// floorParentWindow is the number of parent links floorLagHeaders keeps.
const floorParentWindow = 1024

// recordFloorParent keeps a hash-bound parent link for the floor walk.
// Caller must hold e.mu.
func (e *ConsensusEngine) recordFloorParent(h, parent types.Hash) {
	if !e.floorLag || parent == (types.Hash{}) {
		return
	}
	if e.floorParents == nil {
		e.floorParents = make(map[types.Hash]types.Hash, floorParentWindow)
	}
	if _, ok := e.floorParents[h]; !ok {
		if len(e.floorParentsFIFO) >= floorParentWindow {
			oldest := e.floorParentsFIFO[0]
			e.floorParentsFIFO = e.floorParentsFIFO[1:]
			delete(e.floorParents, oldest)
		}
		e.floorParentsFIFO = append(e.floorParentsFIFO, h)
	}
	e.floorParents[h] = parent
}

// floorParent returns h's parent for the floor walk: importedParents first,
// then the longer floorLagHeaders window.
func (e *ConsensusEngine) floorParent(h types.Hash) (types.Hash, bool) {
	if p, ok := e.importedParents[h]; ok && p != (types.Hash{}) {
		return p, true
	}
	if e.floorLag {
		if p, ok := e.floorParents[h]; ok && p != (types.Hash{}) {
			return p, true
		}
	}
	return types.Hash{}, false
}

// floorWalkLimit is the floor walk's step bound.
func (e *ConsensusEngine) floorWalkLimit() uint64 {
	if e.floorLag {
		return maxFloorWalk + floorParentWindow
	}
	return maxFloorWalk
}
