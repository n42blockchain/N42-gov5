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
	// leader repeats it, and the chain stops making progress. A peer hit this
	// live on the Rust client (leader two imports behind at tenure start, ~0.4 s
	// build, two Decides arriving inside it); the same shape is reachable here
	// and more easily, because blockProductionSyncGate allows a leader two
	// blocks behind to produce and a 22,857-transaction build takes 1.6-2 s.
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

	// Check if quorum already reached (single-validator scenario).
	return e.tryFormPrepareQC()
}

// processProposal processes a proposal from the leader.
func (e *ConsensusEngine) processProposal(proposal *Proposal) error {
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

	// Two-phase mode: Round 1 votes on static validation alone (order-then-
	// execute) — the leader's signature, JustifyQC and DA commitment were
	// verified above; the execution guarantee moves to Round 2 (the
	// CommitVote in processPrepareQC waits for the local import). The
	// extends-rule is still enforced when the parent is already known.
	if e.twoPhaseVote {
		if e.importedBlocks[proposal.BlockHash] && !e.extendsJustify(view, proposal.BlockHash) {
			return nil // extends-rule violation logged; do not vote
		}
		if err := e.journalPrepareVote(view, proposal.BlockHash); err != nil {
			return err // abstain: the commitment is not durable
		}
		return e.sendVote(view, proposal.BlockHash)
	}

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
	log.Info("import-gated vote: deferring until block imported",
		"view", view, "blockHash", proposal.BlockHash)
	return nil
}

// processPrepareQC processes a PrepareQC from the leader.
func (e *ConsensusEngine) processPrepareQC(pqc *PrepareQCMsg) error {
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
	if e.twoPhaseVote && !e.importedBlocks[pqc.BlockHash] {
		held := *pqc
		e.pendingCommitQC = &held
		log.Info("two-phase vote: holding commit vote until block imports",
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
	if e.twoPhaseVote && e.pendingCommitQC != nil && e.pendingCommitQC.BlockHash == blockHash {
		held := e.pendingCommitQC
		e.pendingCommitQC = nil
		if held.View == e.roundState.CurrentView() {
			log.Info("two-phase vote: casting held commit vote after import",
				"view", held.View, "blockHash", blockHash)
			if err := e.processPrepareQC(held); err != nil {
				log.Debug("two-phase held commit vote failed", "err", err)
			}
		}
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
	log.Debug("import-gated vote: block imported but no matching pending proposal", "view", view, "blockHash", blockHash, "hasPending", e.pendingProposals[view])

	return nil
}
