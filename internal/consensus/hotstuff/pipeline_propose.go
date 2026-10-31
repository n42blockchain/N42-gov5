// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Pipelined proposals (N42_PIPELINE_PROPOSE=1): a node enters view v+1 as
// soon as it holds view v's PrepareQC, and view v's Round 2 (commit votes,
// CommitQC, Decide) finishes behind it as a trailing round.

package hotstuff

import (
	"os"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// pipelinePropose is N42_PIPELINE_PROPOSE=1: a node that holds the PrepareQC
// of its current view v -- the leader that formed it, a follower that
// received it, or a follower that finds it as the JustifyQC of view v+1's
// proposal -- locks on it, sends (or holds) its commit vote for v, and enters
// view v+1 at once. The next leader builds its block on v's block during v's
// Round 2 instead of after the CommitQC, which takes Round 2 (~24 ms of a ~70
// ms cycle, E1N26 run 9) off the per-height critical path.
//
// View v's Round 2 is unchanged and finishes as a trailing round: the
// leader's commit collector, a follower's held commit vote and the view's
// timing move into e.trailing, commit votes and the Decide for v are still
// accepted, and the CommitQC commits v's block without moving the view. A
// block commits only through a CommitQC, as before; when v's never forms
// (its leader failed, or v+1's PrepareQC replaced the trailing round first),
// v+1's CommitQC commits it as an ancestor.
//
// Safety. Entering v+1 early is a view advance like any other; the lock
// moves to v's PrepareQC before it, so every later timeout carries a HighQC
// of at least v, and every later vote follows the usual rule (JustifyQC view
// >= lock). A commit vote for v is still cast at most once and only after
// the node locked on v's PrepareQC. A node that timed out in v never enters
// v+1 this way, so no timeout for v comes from a node that commit votes v.
//
// Off by default; not at an epoch boundary (the next epoch is activated
// from the committed reconfiguration, which v's CommitQC stages).
var pipelinePropose = os.Getenv("N42_PIPELINE_PROPOSE") == "1"

// SetPipelineProposeForTest switches pipelinePropose and returns the restore.
func SetPipelineProposeForTest(on bool) func() {
	prev := pipelinePropose
	pipelinePropose = on
	return func() { pipelinePropose = prev }
}

// pipelineSuperseded counts trailing rounds replaced before their CommitQC.
var pipelineSuperseded atomic.Uint64

// trailingRound is view v's Round 2 state once the node entered v+1 early.
type trailingRound struct {
	view      ViewNumber
	blockHash types.Hash

	// Swapped into the engine's own fields while the round is worked on
	// (inTrailing), so the Round 2 handlers run unchanged.
	collector     *VoteCollector
	voteBuf       []pendingVote
	equivocation  map[ValidatorIndex]types.Hash
	timing        ViewTiming
	held          *PrepareQCMsg
	heldJournaled bool

	committed bool
}

// r2View is the view the Round 2 handlers work on: the trailing round's
// inside inTrailing, else the current view.
func (e *ConsensusEngine) r2View() ViewNumber {
	if e.trailingActive {
		return e.trailing.view
	}
	return e.roundState.CurrentView()
}

// pipelineAdvance enters view+1 once this node holds view's PrepareQC for
// blockHash, keeping view's Round 2 as the trailing round. A no-op unless
// the switch is on and view is still current and not timed out.
func (e *ConsensusEngine) pipelineAdvance(view ViewNumber, qc *QuorumCertificate) error {
	if !pipelinePropose || e.trailingActive || view != e.roundState.CurrentView() ||
		e.roundState.TimedOutIn(view) || !e.isMember() {
		return nil
	}
	if e.epochManager.EpochsEnabled() && e.epochManager.IsEpochBoundary(view+1) {
		return nil
	}
	if old := e.trailing; old != nil {
		e.dropTrailing("superseded")
	}
	e.roundState.UpdateLockedQC(qc)
	e.trailing = &trailingRound{
		view:          view,
		blockHash:     qc.BlockHash,
		collector:     e.commitCollector,
		voteBuf:       e.commitVoteBuf,
		equivocation:  e.commitEquivocationTracker,
		timing:        e.viewTiming,
		held:          e.pendingCommitQC,
		heldJournaled: e.pendingCommitJournaled,
	}
	// Detached here so advanceToView's per-view resets leave them alone.
	e.commitCollector = nil
	e.commitVoteBuf = nil
	e.commitEquivocationTracker = make(map[ValidatorIndex]types.Hash)
	e.pendingCommitQC = nil
	e.pendingCommitJournaled = false
	log.Debug("pipeline: entering the next view on the PrepareQC", "view", view, "blockHash", qc.BlockHash)
	if err := e.advanceToView(view + 1); err != nil {
		return err
	}
	return e.emit(EngineOutput{Type: OutputViewChanged, View: e.roundState.CurrentView()})
}

// inTrailing runs fn on the trailing round: its Round 2 state is swapped
// into the engine's fields for the call. A committed round is published and
// cleared afterwards.
func (e *ConsensusEngine) inTrailing(fn func() error) error {
	t := e.trailing
	if t == nil || e.trailingActive {
		return nil
	}
	e.swapTrailing(t)
	e.trailingActive = true
	err := fn()
	e.trailingActive = false
	e.swapTrailing(t)
	if t.committed && e.trailing == t {
		e.publishCommittedTiming(t.timing)
		e.clearTrailing()
	}
	return err
}

func (e *ConsensusEngine) swapTrailing(t *trailingRound) {
	e.commitCollector, t.collector = t.collector, e.commitCollector
	e.commitVoteBuf, t.voteBuf = t.voteBuf, e.commitVoteBuf
	e.commitEquivocationTracker, t.equivocation = t.equivocation, e.commitEquivocationTracker
	e.viewTiming, t.timing = t.timing, e.viewTiming
	e.pendingCommitQC, t.held = t.held, e.pendingCommitQC
	e.pendingCommitJournaled, t.heldJournaled = t.heldJournaled, e.pendingCommitJournaled
}

func (e *ConsensusEngine) clearTrailing() {
	if t := e.trailing; t != nil && t.view < e.roundState.CurrentView() {
		delete(e.pendingJustifyBlocks, t.view)
	}
	e.trailing = nil
}

// dropTrailing abandons an uncommitted trailing round; its block commits as
// an ancestor of a later CommitQC.
func (e *ConsensusEngine) dropTrailing(why string) {
	t := e.trailing
	if t == nil {
		return
	}
	if !t.committed {
		pipelineSuperseded.Add(1)
		log.Info("pipeline: trailing round dropped before its CommitQC", "view", t.view,
			"blockHash", t.blockHash, "why", why, "total", pipelineSuperseded.Load())
	}
	e.clearTrailing()
}

// trailingCommitVote handles a commit vote for the trailing view.
func (e *ConsensusEngine) trailingCommitVote(cv *CommitVote, mt msgTiming) error {
	return e.inTrailing(func() error { return e.processCommitVote(cv, mt) })
}

// trailingCastHeld releases the trailing round's held commit vote once its
// block is attested.
func (e *ConsensusEngine) trailingCastHeld(blockHash types.Hash, gate string) {
	t := e.trailing
	if t == nil || e.trailingActive || t.held == nil || t.held.BlockHash != blockHash {
		return
	}
	_ = e.inTrailing(func() error {
		e.castHeldCommitVoteIfAttested(blockHash, gate)
		return nil
	})
}

// pipelineFromJustify takes the JustifyQC of view current+1's proposal as
// the current view's PrepareQC when that message has not arrived yet, so
// the proposal is processed now instead of waiting in the future buffer.
// Reports whether the view advanced.
func (e *ConsensusEngine) pipelineFromJustify(p *Proposal) bool {
	view := e.roundState.CurrentView()
	if !pipelinePropose || e.trailingActive || p.View != view+1 || p.JustifyQC.View != view ||
		e.prepareQC != nil {
		return false
	}
	pqc := &PrepareQCMsg{View: view, BlockHash: p.JustifyQC.BlockHash, QC: p.JustifyQC}
	if err := e.processPrepareQC(pqc, msgTiming{}); err != nil {
		log.Debug("pipeline: proposal JustifyQC not taken as the PrepareQC", "view", view, "err", err)
		return false
	}
	return e.roundState.CurrentView() == p.View
}
