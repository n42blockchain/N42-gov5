// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Leader-side block proposal emission for HotStuff-2.
// onBlockReady is invoked when the local node (as leader for the
// current view) has a block hash and tx root ready to broadcast.
// Guards non-leaders via IsLeader against the validator set and
// wires the proposal into the engine output channel under the
// current view number.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// onBlockReady is called when this node (as leader) has a block ready to propose.
func (e *ConsensusEngine) onBlockReady(blockHash types.Hash, txRootHash types.Hash) error {
	view := e.roundState.CurrentView()

	if !IsLeader(e.myIndex, view, e.validatorSet()) {
		// The build was triggered for an earlier view we led; by seal time the
		// view rotated. Loud, not silent: this drop means the sealed block will
		// never be proposed and the view can only time out.
		log.Warn("hotstuff: sealed block dropped — not leader for current view",
			"view", view, "block", blockHash.Hex()[:12])
		return nil
	}

	if e.roundState.Phase() != PhaseWaitingForProposal {
		log.Warn("hotstuff: sealed block dropped — phase left WaitingForProposal",
			"view", view, "phase", e.roundState.Phase(), "block", blockHash.Hex()[:12])
		return nil
	}

	// The block was built against the parent chosen when production was
	// TRIGGERED, but justifyQC below is read NOW. A QC arriving during the
	// build moves LockedQC without rotating the view or leaving this phase, so
	// the two can disagree, and the proposal would then pair a newer JustifyQC
	// with a block on an older parent. That is precisely what extendsJustify
	// refuses at vote time: every voter rejects, the view times out, the next
	// leader repeats it, and the chain stops making progress. The window is
	// wide here: blockProductionSyncGate allows a leader two blocks behind to
	// produce, and a 22,857-transaction build takes 1.6-2 s.
	//
	// The comparison catches a SECOND shape with the same test, and that one
	// has been seen live on another client: the builder returning a block that
	// does not extend the parent it was asked for, with the driver trusting the
	// payload it got back. Here the requested parent IS the LockedQC block
	// (service.go passes lq.BlockHash), so "built on the wrong parent" and "the
	// QC moved under the build" both surface as parent != justifyBlock. One
	// check, two failures.
	//
	// (The peer's live halt turned out to be the builder-trust shape, not the
	// QC race — their QC guard fired zero times. This guard was found here by
	// reading, not by reproducing theirs, and it stands on that.)
	//
	// Drop rather than propose, matching the two checks above: a timed-out view
	// is recoverable, a proposal nobody can vote for is not. Fail-open when
	// either side is unknown, exactly as extendsJustify does — the rule
	// tightens as information is available and never blocks the honest path.
	if parent, known := e.importedParents[blockHash]; known && parent != (types.Hash{}) {
		if justifyBlock := e.roundState.LockedQC().BlockHash; justifyBlock != (types.Hash{}) && parent != justifyBlock {
			log.Warn("hotstuff: sealed block dropped — parent no longer extends the current LockedQC",
				"view", view, "block", blockHash.Hex()[:12],
				"blockParent", parent.Hex()[:12], "justifyBlock", justifyBlock.Hex()[:12])
			metricProposalStaleParent.Inc()
			return nil
		}
	}

	// The proposal is signed over the SAME message as a Round 1 vote
	// (SigningMessage(view, blockHash)) and the leader immediately self-votes
	// with it, so proposing IS a vote commitment. Journal it before anything is
	// signed or broadcast; if the journal fails we must not propose at all —
	// the view times out and another leader takes over, which is recoverable,
	// whereas an unjournalled commitment is not.
	if err := e.journalPrepareVote(view, blockHash); err != nil {
		return err
	}

	// S19 (6co): this node is proposing blockHash as leader for view -- record
	// it so advanceToView can release a write latch waiting on
	// journalCommitVote for THIS hash if the view is abandoned (a timeout)
	// before that ever runs. No-op when the switch is off, matching every
	// other leaderWriteAfterJournalEnabled call site.
	if leaderWriteAfterJournalEnabled {
		e.selfProposalHash = blockHash
	}

	justifyQC := e.roundState.LockedQC().Clone()
	message := e.proposalSigningMessage(view, blockHash)
	signature := e.secretKey.Sign(message)
	piggybacked := e.previousPrepareQC
	e.previousPrepareQC = nil

	proposal := &Proposal{
		View:       view,
		BlockHash:  blockHash,
		JustifyQC:  justifyQC,
		Proposer:   e.myIndex,
		Signature:  signature.Marshal(),
		PrepareQC:  piggybacked,
		TxRootHash: txRootHash,
	}

	vs := e.validatorSet()
	e.voteCollector = NewVoteCollector(view, blockHash, vs.Len())
	e.commitCollector = NewVoteCollector(view, blockHash, vs.Len())
	e.roundState.EnterVoting()

	// Leader self-votes (GossipSub doesn't deliver back to sender).
	leaderSig := e.secretKey.Sign(e.voteSigningMessage(view, blockHash))
	if e.voteCollector != nil {
		_ = e.voteCollector.AddVote(e.myIndex, leaderSig)
	}

	now := time.Now()
	e.viewTiming.ProposalSent = &now

	if err := e.emit(EngineOutput{
		Type: OutputBroadcast,
		Message: &ConsensusMsg{
			Type:    MsgProposal,
			Payload: proposal,
		},
	}); err != nil {
		return err
	}

	// Same-leader speculative build: with a leader tenure above one this
	// node also leads view+1, and the vote-time hint never fires for its
	// own block (a leader does not import what it built). Advise the
	// producer now; it waits for the block to persist, then builds view+1
	// on its post-state while the followers import it (round 35l: without
	// this, tenure views proposed in 363-528 ms like rotation views, all
	// twenty builds "triggered (leader view)", none a speculative hit).
	if LeaderForView(view+1, vs) == e.myIndex {
		_ = e.emit(EngineOutput{Type: OutputSpeculativeBuild, View: view + 1, Hash: blockHash})
	}

	// Check if quorum already reached (single-validator scenario).
	return e.tryFormPrepareQC()
}

// processProposal processes a proposal from the leader. receivedAt is S14's
// diagnostic arrival stamp (zero unless N42_CONTENTION_DIAG=1); tLocked is
// taken here, the first line that runs once e.mu is held for this message.
// The deferred recording covers every return path (safety-rule rejection,
// bad signature, etc.), not just the success path.
func (e *ConsensusEngine) processProposal(proposal *Proposal, mt msgTiming) error {
	if contentionDiagEnabled && !mt.arrive.IsZero() {
		tLocked := time.Now()
		defer func() {
			tDone := time.Now()
			e.viewTiming.Contention.proposalLockWait = tLocked.Sub(mt.arrive)
			e.viewTiming.Contention.proposalWork = tDone.Sub(tLocked)
			e.viewTiming.Contention.proposalLockWaitOK = true
			e.viewTiming.Contention.proposalWorkOK = true
		}()
	}
	view := e.roundState.CurrentView()

	if proposal.View != view {
		return &ViewMismatchError{Current: view, Received: proposal.View}
	}

	expectedLeader := LeaderForView(view, e.validatorSet())
	if proposal.Proposer != expectedLeader {
		return &InvalidProposerError{View: view, Expected: expectedLeader, Actual: proposal.Proposer}
	}

	// Verify proposer's BLS signature.
	pk, err := e.validatorSet().GetPublicKey(proposal.Proposer)
	if err != nil {
		return err
	}
	msg := e.proposalSigningMessage(view, proposal.BlockHash)
	if !VerifyBLSSignature(proposal.Signature, pk, msg) {
		return &InvalidSignatureError{View: view, ValidatorIndex: proposal.Proposer}
	}

	// Verify justify_qc aggregate BLS signature (genesis QC is exempt).
	if proposal.JustifyQC.View > 0 {
		if vErr := e.verifyQCAnyDomainWithSet(&proposal.JustifyQC, e.resolveQCValidatorSet(proposal.JustifyQC.View, len(proposal.JustifyQC.Signers))); vErr != nil {
			log.Warn("rejecting proposal with invalid justify_qc",
				"view", view, "proposer", proposal.Proposer, "err", vErr)
			return vErr
		}
	}

	// HotStuff-2 safety rule.
	if !e.roundState.IsSafeToVote(&proposal.JustifyQC) {
		return &SafetyViolationError{
			QCView:     proposal.JustifyQC.View,
			LockedView: e.roundState.LockedQC().View,
		}
	}

	// Tail-fork detection: if the proposal's JustifyQC skips a view where
	// we have a QC (the previous leader produced a valid block but the
	// current leader is ignoring it), log a warning. A full Carry protocol
	// mitigation would require validators to send recent votes to the
	// incoming leader, but detection alone is valuable for monitoring.
	if proposal.JustifyQC.View+1 < view && e.roundState.LockedQC().View == view-1 {
		log.Warn("potential tail-fork: proposal skips previous view QC",
			"view", view, "justifyQC.view", proposal.JustifyQC.View,
			"lockedQC.view", e.roundState.LockedQC().View,
			"proposer", proposal.Proposer)
	}

	e.roundState.UpdateLockedQC(&proposal.JustifyQC)

	// Process piggybacked PrepareQC (chained mode).
	if proposal.PrepareQC != nil {
		if vErr := e.verifyQCWithSet(proposal.PrepareQC, e.resolveQCValidatorSet(proposal.PrepareQC.View, len(proposal.PrepareQC.Signers))); vErr == nil {
			e.roundState.UpdateLockedQC(proposal.PrepareQC)
		} else {
			log.Warn("rejected invalid piggybacked PrepareQC", "view", view, "err", vErr)
		}
	}

	// Baby Raptr: track data availability commitment for post-import verification.
	// The TxRootHash is included in the proposal so that validators can verify
	// transaction data availability after block import.
	if proposal.TxRootHash != (types.Hash{}) && len(e.pendingTxRoots) < MaxImportedBlocks {
		e.pendingTxRoots[proposal.BlockHash] = proposal.TxRootHash
	}

	e.roundState.EnterVoting()
	now := time.Now()
	e.viewTiming.ProposalReceived = &now

	// Request block execution.
	if err := e.emit(EngineOutput{Type: OutputExecuteBlock, Hash: proposal.BlockHash}); err != nil {
		return err
	}

	// Double-vote prevention: only send one Round 1 vote per view.
	if e.roundState.HasVotedInView(view) {
		log.Debug("suppressing duplicate prepare vote", "view", view)
		return nil
	}

	e.pendingProposals[view] = proposal.BlockHash
	e.pendingJustifyBlocks[view] = proposal.JustifyQC.BlockHash

	// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node
	// stored", round 35zzzg): two-phase mode used to vote here immediately,
	// gated only on "extendsJustify if the block happens to be already
	// imported" -- which is essentially never true this early (a Proposal
	// always arrives before the deferred-execution check or the full import
	// completes), so the extends-rule was skipped on the common path. Round 1
	// still only needs the CHEAP, non-executing guarantee ("static
	// validation": CheckDeferredBlock via EventBlockChecked) -- the execution
	// guarantee is Round 2's job either way (processPrepareQC / deferredAttested)
	// -- so two-phase mode now shares the exact same checked/imported gate as
	// import-gated voting below, with extendsJustify enforced every time the
	// block's real parent becomes known instead of only when it happens to be
	// known already.

	// Import-gated voting (NOT optimistic): vote only once the block is imported
	// locally, so a CommitQC proves a quorum actually holds the block — not just
	// its hash. This couples view progress to block propagation; the head can only
	// advance as fast as blocks reach a quorum, which is what stops the engine
	// from spinning thousands of views ahead of the chain. If the block is already
	// imported (a direct push arrived before the Proposal), vote now; otherwise
	// defer until EventBlockImported fires onBlockImported, which casts the vote.
	if e.importedBlocks[proposal.BlockHash] {
		if !e.extendsJustify(view, proposal.BlockHash) {
			return nil // extends-rule violation logged; do not vote
		}
		log.Info("import-gated vote: block already imported, voting now", "view", view, "blockHash", proposal.BlockHash)
		if err := e.journalPrepareVote(view, proposal.BlockHash); err != nil {
			return err // abstain: the commitment is not durable
		}
		return e.sendVote(view, proposal.BlockHash)
	}
	if voted, err := e.tryHeaderVote(view); voted || err != nil {
		return err
	}
	if voted, err := e.tryDeferredVote(view); voted || err != nil {
		return err
	}
	log.Info("import-gated vote: deferring until block imported",
		"view", view, "blockHash", proposal.BlockHash)
	return nil
}

// tryHeaderVote casts the Round 1 prepare vote once this view's pending
// proposal's HEADER is known (S31, docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh):
// extendsJustify only ever reads the block's parent hash, which is a header
// field, so it can be evaluated well before CheckDeferredBlock's own
// per-transaction structural check completes (that check still gates Round
// 2 unchanged, via deferredAttested/checkedBlocks). Two-phase mode only --
// import-gated (non-two-phase) voting keeps its own documented guarantee
// ("vote only once the block is imported locally") and never takes this
// path. Returns whether it voted.
func (e *ConsensusEngine) tryHeaderVote(view ViewNumber) (bool, error) {
	if !e.twoPhaseVote {
		return false, nil
	}
	pending, ok := e.pendingProposals[view]
	if !ok || e.roundState.HasVotedInView(view) {
		return false, nil
	}
	// The header-known map entry must be a REAL, positively-known parent --
	// extendsJustify's own "fail open when parent unknown" branch must not
	// be mistaken for a pass here, or a proposal whose header has not
	// arrived yet would vote blind on the exact fail-open path extendsJustify
	// uses for a genuinely untracked block.
	if parent, known := e.importedParents[pending]; !known || parent == (types.Hash{}) {
		return false, nil
	}
	if !e.extendsJustify(view, pending) {
		return false, nil // extends-rule violation logged; do not vote
	}
	if err := e.journalPrepareVote(view, pending); err != nil {
		return false, err // abstain: the commitment is not durable
	}
	log.Info("header vote: block header known and extends its JustifyQC block, voting",
		"view", view, "blockHash", pending, "tMs", time.Now().UnixMilli())
	return true, e.sendVote(view, pending)
}

// tryDeferredVote casts the prepare vote for view's pending proposal under
// deferred execution: the block was checked (EventBlockChecked) and its
// parent -- the JustifyQC block -- is imported. Returns whether it voted.
func (e *ConsensusEngine) tryDeferredVote(view ViewNumber) (bool, error) {
	pending, ok := e.pendingProposals[view]
	if !ok || !e.checkedBlocks[pending] || e.roundState.HasVotedInView(view) {
		return false, nil
	}
	// S26: a zero-hash justify (the genesis QC, view 0 -- unlocked, nothing
	// committed yet) has no real parent to wait for; treat it the same way
	// extendsJustify itself already fails open for a zero justify, instead of
	// blocking forever on "genesis imports". Any non-zero justify still must
	// be imported before this attestation is trusted.
	justify, ok := e.pendingJustifyBlocks[view]
	if !ok || (justify != (types.Hash{}) && !e.importedBlocks[justify]) {
		return false, nil
	}
	if !e.extendsJustify(view, pending) {
		return false, nil // extends-rule violation logged; do not vote
	}
	if err := e.journalPrepareVote(view, pending); err != nil {
		return false, err // abstain: the commitment is not durable
	}
	log.Info("deferred vote: block checked and parent imported, voting", "view", view, "blockHash", pending, "tMs", time.Now().UnixMilli())
	return true, e.sendVote(view, pending)
}

// deferredAttested reports whether this node's execution guarantee for a block
// is met the deferred way: it checked the block (the header carries this node's
// own result of the parent, and the transactions are includable against that
// post-state) and it has imported the parent. A CommitQC over such votes proves
// a quorum executed the parent and validated this block, which is the guarantee
// deferred execution moves one block back. Without this the Round-2 gate waits
// for the block's own import and the cycle stays import-bound -- 35zzq measured
// the same 1.33 s block time as the round without deferred execution.
func (e *ConsensusEngine) deferredAttested(blockHash types.Hash) bool {
	if !e.checkedBlocks[blockHash] {
		return false
	}
	parent, ok := e.importedParents[blockHash]
	return ok && parent != (types.Hash{}) && e.importedBlocks[parent]
}

// castHeldCommitVoteIfAttested fires a parked Round-2 vote once its block is
// attested (imported, or checked with the parent imported). gate names which
// caller/condition is releasing it -- "own-import" | "parent-import" |
// "checked" (see contentionStamps.commitVoteGate) -- recorded only when the
// vote was actually held; ignored otherwise.
func (e *ConsensusEngine) castHeldCommitVoteIfAttested(blockHash types.Hash, gate string) {
	if !e.twoPhaseVote || e.pendingCommitQC == nil || e.pendingCommitQC.BlockHash != blockHash {
		return
	}
	if !e.importedBlocks[blockHash] && !e.deferredAttested(blockHash) {
		return
	}
	held := e.pendingCommitQC
	e.pendingCommitQC = nil
	if held.View != e.roundState.CurrentView() {
		return
	}
	if contentionDiagEnabled {
		e.viewTiming.Contention.commitVoteGate = gate
	}
	log.Info("two-phase vote: casting held commit vote", "view", held.View, "blockHash", blockHash,
		"deferred", !e.importedBlocks[blockHash], "tMs", time.Now().UnixMilli())
	if err := e.processPrepareQC(held, msgTiming{}); err != nil {
		log.Debug("two-phase held commit vote failed", "err", err)
	}
}

// onBlockChecked records a block the service verified under deferred
// execution and votes for it if its parent is already imported.
func (e *ConsensusEngine) onBlockChecked(blockHash types.Hash, parentHash types.Hash) error {
	if !e.checkedBlocks[blockHash] {
		if len(e.checkedFIFO) >= MaxImportedBlocks {
			oldest := e.checkedFIFO[0]
			e.checkedFIFO = e.checkedFIFO[1:]
			delete(e.checkedBlocks, oldest)
			if !e.importedBlocks[oldest] {
				delete(e.importedParents, oldest) // recorded here for the extends-check only
			}
		}
		e.checkedBlocks[blockHash] = true
		e.checkedFIFO = append(e.checkedFIFO, blockHash)
	}
	if parentHash != (types.Hash{}) {
		e.importedParents[blockHash] = parentHash // the extends-check reads it
	}
	_, err := e.tryDeferredVote(e.roundState.CurrentView())
	e.castHeldCommitVoteIfAttested(blockHash, "checked")
	return err
}

// onBlockHeaderKnown records a block's parent as soon as its HEADER is known
// (S31, docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh) -- well before
// CheckDeferredBlock's own per-transaction check completes, and possibly
// before the body's transactions have even finished decoding. This does NOT
// set checkedBlocks: Round 2's execution guarantee (deferredAttested) is
// completely unaffected and still requires the real check. Bounded by its
// own FIFO, since a block may arrive here without ever being checked or
// imported (a stale/abandoned header) and must not pin importedParents
// forever; an entry already tracked by checkedBlocks/importedBlocks is left
// alone by this FIFO's own eviction, matching checkedFIFO's existing guard.
func (e *ConsensusEngine) onBlockHeaderKnown(blockHash, parentHash types.Hash, _ uint64) error {
	if parentHash == (types.Hash{}) {
		return nil // nothing to record; extendsJustify already fails open for this case
	}
	if _, known := e.importedParents[blockHash]; !known {
		if len(e.headerKnownFIFO) >= MaxImportedBlocks {
			oldest := e.headerKnownFIFO[0]
			e.headerKnownFIFO = e.headerKnownFIFO[1:]
			if !e.checkedBlocks[oldest] && !e.importedBlocks[oldest] {
				delete(e.importedParents, oldest)
			}
		}
		e.headerKnownFIFO = append(e.headerKnownFIFO, blockHash)
	}
	e.importedParents[blockHash] = parentHash
	_, err := e.tryHeaderVote(e.roundState.CurrentView())
	return err
}

// processPrepareQC processes a PrepareQC from the leader.
// processPrepareQC processes an incoming PrepareQC and, once the two-phase
// gate is satisfied, sends the Round 2 commit vote. receivedAt is S14's
// diagnostic arrival stamp for a FRESH message; it is zero when this call is
// a held-vote release re-entry from castHeldCommitVoteIfAttested, in which
// case the original arrival's lockWait/work/arrival were already recorded on
// first entry and must not be overwritten here.
func (e *ConsensusEngine) processPrepareQC(pqc *PrepareQCMsg, mt msgTiming) error {
	if contentionDiagEnabled && !mt.arrive.IsZero() {
		tLocked := time.Now()
		e.viewTiming.Contention.prepareQCArrival = mt.arrive
		defer func() {
			tDone := time.Now()
			e.viewTiming.Contention.prepareQCLockWait = tLocked.Sub(mt.arrive)
			e.viewTiming.Contention.prepareQCWork = tDone.Sub(tLocked)
			e.viewTiming.Contention.prepareQCLockWaitOK = true
			e.viewTiming.Contention.prepareQCWorkOK = true
		}()
		// S17: receiver-side t_rx->t_arrive for the one PrepareQC message
		// this view. First arrival wins; a duplicate via the OTHER
		// transport ("gossip is always sent" regardless of Rotor's own
		// success, service.go) only flips Via to "both" and counts as a
		// dup, never overwriting the first timing.
		if !mt.rx.IsZero() {
			rx := &e.viewTiming.Contention.rx
			if !rx.seenPrepareQC {
				rx.seenPrepareQC = true
				rx.pqcRx2Arr = mt.arrive.Sub(mt.rx)
				if rx.pqcRx2Arr < 0 {
					rx.pqcRx2Arr = 0
				}
				rx.pqcRxAtMs = mt.rx.UnixMilli()
				rx.pqcVia = mt.via
				rx.pqcOK = true
			} else {
				rx.dupN++
				if rx.pqcVia != "" && rx.pqcVia != mt.via {
					rx.pqcVia = "both"
				}
			}
		}
	}
	view := e.roundState.CurrentView()

	if pqc.View != view {
		return &ViewMismatchError{Current: view, Received: pqc.View}
	}

	if err := e.verifyQCWithSet(&pqc.QC, e.resolveQCValidatorSet(pqc.QC.View, len(pqc.QC.Signers))); err != nil {
		return err
	}

	// Double-vote prevention: only send one Round 2 commit vote per view.
	if e.roundState.HasCommitVotedInView(view) {
		log.Debug("suppressing duplicate commit vote", "view", view)
		return nil
	}

	// Two-phase R2 gate: the CommitVote is the execution attestation — hold
	// it until the block is imported locally, so a CommitQC still proves
	// 2f+1 validators EXECUTED the block. onBlockImported re-enters this
	// function with the held message once the import lands.
	if e.twoPhaseVote && !e.importedBlocks[pqc.BlockHash] && !e.deferredAttested(pqc.BlockHash) {
		held := *pqc
		e.pendingCommitQC = &held
		if contentionDiagEnabled {
			e.viewTiming.Contention.commitVoteHeld = true
		}
		log.Info("two-phase vote: holding commit vote until block imports",
			"view", view, "blockHash", pqc.BlockHash)
		return nil
	}

	// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node
	// stored"): defense in depth for the Round 1 fix above. A valid
	// PrepareQC signature only proves 2f+1 validators SENT a prepare vote --
	// not that the block they voted for actually extends the chain. By this
	// point the block's real parent is known (imported, or checked with
	// deferredAttested true), so extendsJustify can resolve for real instead
	// of failing open; refuse the commit vote if it does not extend its own
	// JustifyQC block (recorded from this view's Proposal).
	if !e.extendsJustify(view, pqc.BlockHash) {
		log.Warn("commit vote REFUSED: proposal does not extend its JustifyQC block",
			"view", view, "blockHash", pqc.BlockHash)
		return nil
	}

	if !e.isMember() {
		return nil // observer/removed nodes do not cast commit votes
	}

	e.roundState.UpdateLockedQC(&pqc.QC)
	e.roundState.EnterPreCommit()

	// Journal the Round 2 commitment BEFORE signing/sending it. The snapshot
	// also carries the lock just advanced above, so the lock and the vote it
	// justifies become durable together — a restart can never resume with a
	// commit vote on disk and a staler lock, or vice versa.
	if err := e.journalCommitVote(view, pqc.BlockHash); err != nil {
		return err // abstain: the commitment is not durable
	}

	// Send CommitVote (Round 2).
	commitMsg := e.commitSigningMessage(view, pqc.BlockHash)
	commitSig := e.secretKey.Sign(commitMsg)
	leader := LeaderForView(view, e.validatorSet())

	commitVote := &CommitVote{
		View:      view,
		BlockHash: pqc.BlockHash,
		Voter:     e.myIndex,
		Signature: commitSig.Marshal(),
		HighTC:    e.roundState.HighestTC(),
	}

	now := time.Now()
	e.viewTiming.CommitVoteSent = &now
	// The commitment was already recorded (and made durable) by
	// journalCommitVote above, which also prevents a re-attempt.

	return e.emit(EngineOutput{
		Type:   OutputSendToValidator,
		Target: leader,
		Message: &ConsensusMsg{
			Type:    MsgCommitVote,
			Payload: commitVote,
		},
	})
}

// sendVote sends a Round 1 vote for the given view and block hash.
func (e *ConsensusEngine) sendVote(view ViewNumber, blockHash types.Hash) error {
	if !e.isMember() {
		return nil // observer/removed nodes do not cast votes
	}
	leader := LeaderForView(view, e.validatorSet())
	voteMsg := e.voteSigningMessage(view, blockHash)
	voteSig := e.secretKey.Sign(voteMsg)

	vote := &Vote{
		View:      view,
		BlockHash: blockHash,
		Voter:     e.myIndex,
		Signature: voteSig.Marshal(),
		HighTC:    e.roundState.HighestTC(),
	}

	now := time.Now()
	e.viewTiming.VoteSent = &now

	if err := e.emit(EngineOutput{
		Type:   OutputSendToValidator,
		Target: leader,
		Message: &ConsensusMsg{
			Type:    MsgVote,
			Payload: vote,
		},
	}); err != nil {
		return err
	}

	// Cross-view speculative build hint: if round-robin makes this node the
	// NEXT view's leader, its proposal will extend the block just voted for
	// (happy path). The block is already imported here — import-gated voting
	// guarantees the post-state this build needs — so the ~500ms build can run
	// during this view's vote rounds instead of after the view change. Gated
	// on importedBlocks because the two-phase mode votes before import.
	// Advisory only; see OutputSpeculativeBuild.
	if e.importedBlocks[blockHash] && LeaderForView(view+1, e.validatorSet()) == e.myIndex {
		_ = e.emit(EngineOutput{Type: OutputSpeculativeBuild, View: view + 1, Hash: blockHash})
	}
	return nil
}

// rememberImported records that this block is locally imported, retained across
// view changes (bounded FIFO). Because importedBlocks now survives view changes,
// it needs its own eviction: evict the oldest hash once at MaxImportedBlocks so a
// new import is always recorded (a stuck round re-imports the SAME hash, which
// occupies one slot, so the recent-block window is never starved).
func (e *ConsensusEngine) rememberImported(h types.Hash, parent types.Hash) {
	if e.importedBlocks[h] {
		if parent != (types.Hash{}) {
			e.importedParents[h] = parent // backfill if the first notify lacked it
		}
		return
	}
	if len(e.importedFIFO) >= MaxImportedBlocks {
		oldest := e.importedFIFO[0]
		e.importedFIFO = e.importedFIFO[1:]
		delete(e.importedBlocks, oldest)
		delete(e.importedParents, oldest)
	}
	e.importedBlocks[h] = true
	if parent != (types.Hash{}) {
		e.importedParents[h] = parent
	}
	e.importedFIFO = append(e.importedFIFO, h)
}

// extendsJustify enforces the HotStuff extends rule at vote time: the proposed
// block's parent must be the proposal's JustifyQC block. Without it a proposal
// can pair a high-view JustifyQC with a block on a DIFFERENT branch and voters
// certify a conflicting chain (observed live: a dead same-height sibling
// re-proposed at a committed height seeded conflicting commits at 13014242).
// Fail-open when either side is unknown (genesis justify, parent not tracked) —
// the rule tightens as information is available, never blocks the honest path.
func (e *ConsensusEngine) extendsJustify(view ViewNumber, blockHash types.Hash) bool {
	justify, ok := e.pendingJustifyBlocks[view]
	if !ok || justify == (types.Hash{}) {
		return true
	}
	parent, known := e.importedParents[blockHash]
	if !known || parent == (types.Hash{}) {
		return true
	}
	if parent != justify {
		log.Warn("import-gated vote REFUSED: proposal does not extend its JustifyQC block",
			"view", view, "blockHash", blockHash,
			"blockParent", parent, "justifyBlock", justify)
		return false
	}
	return true
}

// onBlockImported handles the BlockImported event and verifies DA commitment.
func (e *ConsensusEngine) onBlockImported(blockHash types.Hash, actualTxRoot types.Hash, parentHash types.Hash) error {
	e.rememberImported(blockHash, parentHash)

	// Baby Raptr DA verification: compare the proposal's TxRootHash with
	// the actual transaction root computed during block import.
	if expectedTxRoot, ok := e.pendingTxRoots[blockHash]; ok {
		delete(e.pendingTxRoots, blockHash)
		if actualTxRoot != (types.Hash{}) && expectedTxRoot != actualTxRoot {
			log.Warn("DA verification failed: TxRootHash mismatch",
				"blockHash", blockHash,
				"expected", expectedTxRoot,
				"actual", actualTxRoot,
			)
			return &DAVerificationError{
				BlockHash:    blockHash,
				ExpectedRoot: expectedTxRoot,
				ActualRoot:   actualTxRoot,
			}
		}
		log.Debug("DA verification passed", "blockHash", blockHash, "txRoot", expectedTxRoot)
	}

	// Two-phase mode: a held Round-2 CommitVote fires as soon as its block
	// imports (processPrepareQC parked it; re-entering is idempotent via
	// HasCommitVotedInView).
	if e.twoPhaseVote && e.pendingCommitQC != nil {
		// This block, or a checked child of it whose guarantee this import
		// completes (deferred execution attests the parent, not the block).
		// blockHash (this onBlockImported call's own parameter) equal to the
		// held vote's block means THIS block's own import satisfied the
		// gate ("own-import"); any other value means some other import --
		// in practice the parent's -- made deferredAttested newly true
		// ("parent-import").
		gate := "parent-import"
		if blockHash == e.pendingCommitQC.BlockHash {
			gate = "own-import"
		}
		e.castHeldCommitVoteIfAttested(e.pendingCommitQC.BlockHash, gate)
	}

	// Import-gated voting: now that this block is imported, cast the deferred
	// prepare vote if it is the block we are waiting to vote on in the current
	// view (recorded by processProposal). This is what advances the round once
	// the block has actually propagated to and been imported by us.
	view := e.roundState.CurrentView()
	if pending, ok := e.pendingProposals[view]; ok && pending == blockHash &&
		!e.roundState.HasVotedInView(view) {
		if !e.extendsJustify(view, blockHash) {
			return nil // extends-rule violation logged; do not vote
		}
		if err := e.journalPrepareVote(view, blockHash); err != nil {
			return err // abstain: the commitment is not durable
		}
		log.Info("import-gated vote: casting deferred vote after import", "view", view, "blockHash", blockHash)
		return e.sendVote(view, blockHash)
	}
	// The no-op branch: an import that is not the current view's pending
	// proposal, which is the common case (the leader importing its own block,
	// catch-up imports, a re-import after a view change). It carried Info and
	// fired on nearly every import, making it 8% of all log bytes.
	// Deferred execution: the imported block may be the PARENT of the
	// pending, already-checked proposal.
	if voted, err := e.tryDeferredVote(view); voted || err != nil {
		return err
	}
	log.Debug("import-gated vote: block imported but no matching pending proposal", "view", view, "blockHash", blockHash, "hasPending", e.pendingProposals[view])

	return nil
}

// onBlockRejected withdraws the deferred-execution check evidence of a
// block that failed on import (n42-rs found a rejected block keeping it:
// a re-proposal of the hash was voted for unchecked).
func (e *ConsensusEngine) onBlockRejected(blockHash types.Hash) {
	if !e.checkedBlocks[blockHash] {
		return
	}
	delete(e.checkedBlocks, blockHash)
	for i, h := range e.checkedFIFO {
		if h == blockHash {
			e.checkedFIFO = append(e.checkedFIFO[:i], e.checkedFIFO[i+1:]...)
			break
		}
	}
	if !e.importedBlocks[blockHash] {
		delete(e.importedParents, blockHash)
	}
	log.Warn("deferred vote evidence withdrawn: the checked block failed on import", "blockHash", blockHash)
}
