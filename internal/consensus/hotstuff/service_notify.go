// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service block import/commit notifications and fetch-on-miss handling.

package hotstuff

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// handleBlockCommitted canonicalizes a committed block and persists the
// consensus state with it. Runs on the output loop, or on the commit loop
// under the lag gate (commitAsync).
func (s *Service) handleBlockCommitted(output EngineOutput) {
	log.Info("hotstuff: block committed", "view", output.View, "hash", output.Hash)
	// Post-commit phase timings — OBSERVABILITY ONLY. Both steps run inline
	// on the serial output loop and each opens its own MDBX write tx (a
	// second and third fsync for the block), so they belong in the
	// block-write cost picture alongside "blockwrite phases".
	tObserve := time.Now()
	if s.availableHere(output.Hash) {
		// P2a: committed on availability; its execution runs behind in
		// the lag executor, which retries the canonical commit on import.
	} else if applied, _, number := s.observeCommittedExecution(output.Hash, true); !applied {
		s.requestCommittedCatchUp(output.Hash, number)
	}
	dObserve := time.Since(tObserve)
	var dCanon, dPersist time.Duration
	// Make the committed block the canonical head so every node's chain follows
	// the single committed chain (the miner may have locally inserted a
	// different same-height candidate via resultLoop). Skipped silently if the
	// block isn't in the DB yet — it reconciles on a later commit / future-block
	// import once the block arrives via direct push.
	// Mark pending reconfigurations as committed now that the block has a
	// CommitQC. Hoisted ABOVE canonicalization so the state snapshot taken
	// below observes it: that snapshot must be resolved before any write
	// transaction opens, and the canonicalization transaction is now where
	// the state gets written. Safe to move — the CommitQC is what makes the
	// reconfiguration committed, and it exists here whether or not the local
	// canonical table has caught up; this ran unconditionally before too.
	if rm := s.engine.Engine().ReconfigManager(); rm != nil && rm.HasPendingChanges() {
		rm.MarkCommitted()
	}
	// Persist the consensus state INSIDE the canonicalization transaction
	// when the producer supports it: one write transaction per committed
	// block instead of two. It also closes a crash window — canonical head
	// and consensus state now move together, where a crash between the two
	// transactions used to leave a node whose persisted view/lock did not
	// match its applied chain.
	hook := s.newStateHook()
	cw, canMerge := s.blockProducer.(CanonicalCommitterWithTx)
	var cErr error
	if s.blockProducer != nil {
		tCanon := time.Now()
		if canMerge && hook != nil {
			cErr = cw.CommitToCanonicalWith(output.Hash, hook.run)
		} else {
			cErr = s.blockProducer.CommitToCanonical(output.Hash)
		}
		if cErr != nil {
			log.Debug("hotstuff: commit-to-canonical deferred", "hash", output.Hash, "err", cErr)
			// Remember it — NotifyBlockImported retries when the block lands.
			s.pendingMu.Lock()
			if s.pendingCommits == nil || len(s.pendingCommits) >= maxPendingCommits {
				s.pendingCommits = make(map[types.Hash]struct{})
			}
			s.pendingCommits[output.Hash] = struct{}{}
			s.pendingMu.Unlock()
		} else {
			s.pendingMu.Lock()
			delete(s.pendingCommits, output.Hash)
			// A committed block extends the recovered one, and the walk
			// just canonicalized its lineage.
			s.recoveredCommit = types.Hash{}
			s.pendingMu.Unlock()
			// The committed floor just advanced: a vote held on an
			// unresolved ancestry may now resolve (N42_VOTE_DEFER_RETRY).
			if ce := s.engine.Engine(); ce != nil {
				ce.RecheckDeferredVotes()
			}
		}
		dCanon = time.Since(tCanon)
	}
	updateMetricsBlockCommitted(output.View)
	tPersist := time.Now()
	// The hook must have completed AND the enclosing transaction must have
	// committed. Update invokes the hook before tx.Commit, so hook.done alone
	// can be true when the final MDBX commit fails and rolls every write back.
	// In every other case the state still has to reach disk, so fall back to
	// its own transaction.
	if stateHookCommitted(hook, cErr) {
		s.setLastPersistedView(hook.view)
	} else {
		s.persistState()
	}
	dPersist = time.Since(tPersist)
	log.Info("hotstuff: commit phases",
		"view", output.View, "observe", dObserve, "canon", dCanon, "persist", dPersist,
		"total", time.Since(tObserve))

	// Process pending equivocation slashing.
	if s.slashingExecutor != nil {
		if n, err := s.slashingExecutor.ProcessPendingSlashing(s.ctx); err != nil {
			log.Error("hotstuff: slashing execution failed", "err", err)
		} else if n > 0 {
			log.Warn("hotstuff: validators slashed for equivocation", "count", n)
		}
	}

	// Block randomness is NOT fed to the EVM from here anymore: the
	// 0x0302 beacon reads the header's PrevRanDao (consensus-committed,
	// identical for leader/followers/replay). A process-local feed keyed
	// by whatever QC this node last observed diverged between roles and
	// was unreproducible at replay.
}

// fetchOnMissGrace is how long a follower waits for the direct push of a
// proposed block it does not hold before fetching it: the push grace's
// counterpart. Zero (fetch at once) unless the leader sends the Proposal
// without a push grace.
func fetchOnMissGrace() time.Duration {
	if proposalPushGrace() == 0 {
		return 100 * time.Millisecond
	}
	return 0
}

// scheduleFetchOnMiss fetches hash after d unless, by then, the block has
// arrived (held available or imported) or the view moved on.
func (s *Service) scheduleFetchOnMiss(hash types.Hash, d time.Duration) {
	time.AfterFunc(d, func() {
		if (s.ctx != nil && s.ctx.Err() != nil) || s.blockFetcher == nil || s.availableHere(hash) {
			return
		}
		s.pendingMu.Lock()
		_, pending := s.pendingExecutions[hash]
		s.pendingMu.Unlock()
		if !pending {
			return
		}
		s.blockFetcher.FetchBlockByHash(hash)
	})
}

// legacyProposalPushGrace is the pause the leader takes between handing its
// sealed block to the direct push and publishing the Proposal, when the vote
// waits for the block.
const legacyProposalPushGrace = 50 * time.Millisecond

// proposalPushGrace is how long the leader holds a Proposal after the block's
// direct push was dispatched. Under vote on availability it is zero: a
// follower votes on the EventBlockChecked / header-known events whichever of
// the Proposal and the push lands first (processProposal defers the vote,
// onBlockChecked casts it), so the pause only added a fixed 50 ms to every
// view's Round 1 (the r1kth floor of 55 ms measured on empty blocks).
// N42_PROPOSAL_PUSH_GRACE_MS overrides both modes (0 = no pause).
func proposalPushGrace() time.Duration {
	if ms, ok := proposalPushGraceEnv(); ok {
		return time.Duration(ms) * time.Millisecond
	}
	if block.VoteOnAvailability() {
		return 0
	}
	return legacyProposalPushGrace
}

var (
	proposalPushGraceOnce sync.Once
	proposalPushGraceMs   int64
	proposalPushGraceSet  bool
)

func proposalPushGraceEnv() (int64, bool) {
	proposalPushGraceOnce.Do(func() {
		v := os.Getenv("N42_PROPOSAL_PUSH_GRACE_MS")
		if v == "" {
			return
		}
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms < 0 {
			log.Warn("ignoring invalid N42_PROPOSAL_PUSH_GRACE_MS", "value", v)
			return
		}
		proposalPushGraceMs, proposalPushGraceSet = ms, true
	})
	return proposalPushGraceMs, proposalPushGraceSet
}

// maxPendingCommits bounds the deferred-commit set; a node that far behind
// catches up through the range import, not through retries.
const maxPendingCommits = 256

// NotifyBlockRejected implements sync.BlockImportNotifier: a block that
// failed validation on import withdraws any deferred-execution check
// evidence, so a re-proposal of the same hash is not voted for on the
// strength of a check whose block then failed.
func (s *Service) NotifyBlockRejected(hash types.Hash) {
	if s.engine == nil {
		return
	}
	if ce := s.engine.Engine(); ce != nil {
		if err := ce.ProcessEvent(ConsensusEvent{Type: EventBlockRejected, Hash: hash}); err != nil {
			log.Debug("hotstuff: EventBlockRejected processing failed", "hash", hash, "err", err)
		}
	}
}

// NotifyBlockChecked implements sync.BlockImportNotifier: under deferred
// execution the sync layer verified an arrived block without executing it
// (its header carries this node's result of the parent; its transactions
// are includable), so the engine may vote for it as soon as the parent is
// imported.
func (s *Service) NotifyBlockChecked(hash types.Hash, parent types.Hash) {
	if s.lagGate != nil {
		// A lag-gated view waiting for its parent's header (not held yet)
		// can go on once the parent is checked available.
		s.resumeDeferredProduction(hash, "availability")
	}
	if ce := s.engine.Engine(); ce != nil {
		if err := ce.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: hash, ParentHash: parent}); err != nil {
			log.Debug("hotstuff: EventBlockChecked processing failed", "hash", hash, "err", err)
		}
	}
}

// NotifyBlockHeaderKnown implements sync.BlockImportNotifier (S31,
// docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh): the block-push receive path has
// decoded this block's header, well before CheckDeferredBlock's own
// per-transaction check (or even the rest of the body) has been processed.
// extendsJustify only ever reads the parent hash, so a two-phase Round 1
// prepare vote may fire on this alone; Round 2's own execution guarantee is
// untouched.
func (s *Service) NotifyBlockHeaderKnown(hash types.Hash, parent types.Hash, number uint64) {
	if ce := s.engine.Engine(); ce != nil {
		if err := ce.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown, Hash: hash, ParentHash: parent, Number: number}); err != nil {
			log.Debug("hotstuff: EventBlockHeaderKnown processing failed", "hash", hash, "err", err)
		}
	}
}

// NotifyLagResultReady: hash's executed result is known on this node (its
// write may still be in flight, P12 N42_LAG_GATE_APPLIED). A leader view the
// lag gate parked on it re-runs the gate now instead of at durability.
func (s *Service) NotifyLagResultReady(hash types.Hash) {
	s.resumeDeferredProduction(hash, "applied")
}

// NotifyBlockImported implements sync.BlockImportNotifier.
// Called by the sync layer after a gossip block is successfully imported.
// Matches against pending execution requests and notifies the engine.
func (s *Service) NotifyBlockImported(hash types.Hash, txHash types.Hash) {
	// Import notifications are execution retry opportunities for a committed
	// block. A successful applied probe clears the production guard; a body-only
	// or future-queued notification counts as another failed observation.
	s.pendingMu.Lock()
	trackExecution := s.unexecutedCommitHash == hash && hash != (types.Hash{})
	s.pendingMu.Unlock()
	if trackExecution {
		s.observeCommittedExecution(hash, false)
	}

	s.pendingMu.Lock()
	delete(s.pendingExecutions, hash) // clear if present; no longer used to gate
	_, retryCommit := s.pendingCommits[hash]
	if retryCommit {
		delete(s.pendingCommits, hash)
	}
	s.pendingMu.Unlock()

	// The block a deferred leader view was waiting for (the parent to
	// extend, or under the lag gate the block it must have executed) has
	// just been applied locally: re-run the gate, if this node still leads
	// that view.
	s.resumeDeferredProduction(hash, "import")

	// A commit that was deferred because this block hadn't arrived: finish it
	// now, so the canonical marker and head advance on every node, not just the
	// ones that held the block at commit time. (Not applied-gated: the block
	// already holds a CommitQC — canonicalizing it needs its body, not its
	// local execution.)
	if retryCommit && s.blockProducer != nil {
		if err := s.blockProducer.CommitToCanonical(hash); err != nil {
			log.Debug("hotstuff: deferred commit-to-canonical retry failed", "hash", hash, "err", err)
		} else {
			log.Info("hotstuff: deferred commit-to-canonical completed", "hash", hash)
		}
	}
	// The restart's pending commit (recovered_commit.go).
	s.retryRecoveredCommit(hash)

	// Best-effort parent lookup for the engine's extends-check (vote must only
	// certify a block that extends its proposal's JustifyQC block). Zero when
	// unavailable — the check fails open.
	var parentHash types.Hash
	var headerExtra []byte
	var blockNum uint64
	haveHeader := false
	if s.db != nil {
		_ = s.db.View(s.ctx, func(tx kv.Tx) error {
			if hdr, herr := rawdb.ReadHeaderByHash(tx, hash); herr == nil && hdr != nil {
				parentHash = hdr.ParentHash
				headerExtra = hdr.Extra
				blockNum = hdr.Number.Uint64()
				haveHeader = true
			}
			return nil
		})
	}
	// Applied-evidence gate: the engine treats EventBlockImported as "this
	// node EXECUTED the block", and the import-gated vote certifies exactly
	// that. Callers historically fired it on body arrival or a nil insert
	// error — a future-queued block returns nil too — which let six
	// validators vote a forked leader's inexecutable block into a CommitQC
	// and wedge the whole network's committed head. No applied state, no
	// engine notification; the dedup set is NOT stamped, so the notify that
	// arrives after the block really executes still passes.
	if !haveHeader {
		return
	}
	if bf, ok := s.blockFetcher.(interface {
		BlockApplied(types.Hash, uint64) bool
	}); ok && !bf.BlockApplied(hash, blockNum) {
		log.Debug("hotstuff: import notification withheld (block not applied)",
			"hash", hash, "number", blockNum)
		return
	}
	// Persistence may have finished after the view's initial leader gate. Do
	// this before vote dedup: NotifyBlockExecuted can have certified execution
	// while the parent was still unavailable to the miner's read transaction.
	select {
	case s.productionRetry <- struct{}{}:
	default:
	}

	// Dedup the engine notification: skip if we already notified this hash.
	// The engine keeps its own imported-block memory across views, so one
	// notify per hash is enough to let the import-gated prepare vote fire;
	// re-notifying on every re-gossip only spins the engine mutex and starves
	// the vote path.
	s.pendingMu.Lock()
	alreadyNotified := false
	if _, ok := s.notifiedImports[hash]; ok {
		alreadyNotified = true
	} else {
		s.notifiedImports[hash] = struct{}{}
		s.notifiedFIFO = append(s.notifiedFIFO, hash)
		if len(s.notifiedFIFO) > maxNotifiedImports {
			oldest := s.notifiedFIFO[0]
			s.notifiedFIFO = s.notifiedFIFO[1:]
			delete(s.notifiedImports, oldest)
		}
	}
	s.pendingMu.Unlock()

	// Notify the engine once per hash (dedup above). Do NOT gate on
	// pendingExecutions, which advanceToView clears every view. With import-gated
	// voting, the engine's onBlockImported decides (via pendingProposals) whether
	// this import unblocks the current view's deferred prepare vote. A re-gossiped
	// already-notified block carries no new information for the engine — the
	// engine retains imported-block memory across views — so re-notifying only
	// spins the engine mutex.
	if alreadyNotified {
		return
	}
	// Catch-up canonicalization: the imported block's extra carries the leader's
	// latest CommitQC — the network's transferable, BLS-verifiable proof that the
	// QC's target block is committed. Live commits flow through
	// OutputBlockCommitted, but a node importing HISTORICAL blocks (range
	// catch-up, future-queue drain) sees views that are long gone, so this is its
	// only legitimate way to advance the canonical head — import-time fork choice
	// is disabled on leader-driven chains (canonical is commit-authority-only).
	s.maybeCommitFromHeaderQC(headerExtra)
	if ce := s.engine.Engine(); ce != nil {
		// Observer/removed-node reconfig commit MUST run BEFORE processing the
		// imported block. Such a node produces no commits, so its reconfigMgr never
		// sees OutputBlockCommitted (which drives MarkCommitted for active
		// validators); importing a committed block is its evidence that consensus
		// progressed past the pending change. ProcessEvent(EventBlockImported) below
		// may cross an epoch boundary and try to apply the pending reconfig, which
		// requires IsCommitted already true — marking AFTER would always miss the
		// boundary-crossing import and delay activation by a full epoch, leaving the
		// node a phantom member (counted by the active set but not participating).
		if ce.IsRemoved() {
			if rm := ce.ReconfigManager(); rm != nil && rm.HasPendingChanges() {
				rm.MarkCommitted()
			}
		}
		if err := ce.ProcessEvent(ConsensusEvent{
			Type:       EventBlockImported,
			Hash:       hash,
			TxRootHash: txHash,
			ParentHash: parentHash,
		}); err != nil {
			log.Debug("hotstuff: EventBlockImported processing failed", "hash", hash, "err", err)
		}
	}
}

// NotifyBlockExecuted is the EARLY vote path: the blockchain calls it the
// moment a block's execution and state-root validation succeed — BEFORE the
// block is persisted. The import-gated vote certifies "this node executed and
// validated the block", and at this call site that is true by construction
// (same call stack, err==nil), which is STRONGER evidence than the
// BlockApplied database probe NotifyBlockImported must rely on for
// notifications of unknown provenance. Persistence (~150-250ms of MDBX commit
// on a saturated block) then overlaps the vote round instead of preceding it.
//
// The header fields are passed in rather than read from the DB because the
// header is not written yet. Deduplication is shared with
// NotifyBlockImported, so whichever notification arrives second is a no-op;
// a crash between the vote and the persist costs this node a re-import on
// restart and nothing else — HotStuff quorum safety never depended on a
// voter's local durability (it tolerates f arbitrary faults).
func (s *Service) NotifyBlockExecuted(hash, txHash, parentHash types.Hash, number uint64, extra []byte) {
	s.pendingMu.Lock()
	if _, ok := s.notifiedImports[hash]; ok {
		s.pendingMu.Unlock()
		return
	}
	s.notifiedImports[hash] = struct{}{}
	s.notifiedFIFO = append(s.notifiedFIFO, hash)
	if len(s.notifiedFIFO) > maxNotifiedImports {
		oldest := s.notifiedFIFO[0]
		s.notifiedFIFO = s.notifiedFIFO[1:]
		delete(s.notifiedImports, oldest)
	}
	s.pendingMu.Unlock()

	s.maybeCommitFromHeaderQC(extra)
	if ce := s.engine.Engine(); ce != nil {
		if ce.IsRemoved() {
			if rm := ce.ReconfigManager(); rm != nil && rm.HasPendingChanges() {
				rm.MarkCommitted()
			}
		}
		if err := ce.ProcessEvent(ConsensusEvent{
			Type:       EventBlockImported,
			Hash:       hash,
			TxRootHash: txHash,
			ParentHash: parentHash,
		}); err != nil {
			log.Debug("hotstuff: EventBlockImported (early) processing failed", "hash", hash, "err", err)
		}
	}
}

// maybeCommitFromHeaderQC advances the canonical chain from the CommitQC a
// just-imported block carries in its header extra (buildHeaderExtra embeds the
// leader's LastCommittedQC into every block). Import-time fork choice is
// disabled on leader-driven chains, so a catching-up node's canonical head can
// only follow commit proofs; the embedded QC is exactly that — a 2f+1
// BLS-verifiable statement that its target block is committed — and needs no
// live view context. Cheap height gate first: signature verification runs only
// when the QC's target is locally known AND ahead of the committed head, so the
// synced steady state pays one header read per import. A QC signed by an older
// validator set (cross-epoch catch-up) fails verification and is skipped —
// fail-closed; such a node converges via live commits once its view syncs.
func (s *Service) maybeCommitFromHeaderQC(extra []byte) {
	if s.blockProducer == nil || s.db == nil || len(extra) == 0 {
		return
	}
	qc, err := ExtractHeaderQC(extra)
	if err != nil || qc == nil || qc.View == 0 || qc.BlockHash == (types.Hash{}) {
		return
	}
	var qcNum *uint64
	var committedHead uint64
	_ = s.db.View(s.ctx, func(tx kv.Tx) error {
		qcNum = rawdb.ReadHeaderNumber(tx, qc.BlockHash)
		if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil {
			committedHead = *hn
		}
		return nil
	})
	if qcNum == nil || *qcNum <= committedHead {
		return // target not imported yet, or not ahead of the committed head
	}
	ce := s.engine.Engine()
	if ce == nil {
		return
	}
	// Canonicalization must follow a COMMIT proof only. The producer embeds
	// LastCommittedQC (commit domain) here, so require the commit domain:
	// VerifyQCAnyDomain would also accept a PrepareQC, and a PrepareQC can
	// form for a block that is later abandoned (leader crash, lower-highQC TC,
	// a conflicting block committing at that height). Accepting one would let a
	// crafted header embedding a stale PrepareQC drive a catching-up node to
	// canonicalize a never-committed block, diverging its canonical head.
	if verr := ce.VerifyCommitQCWithResolvedSet(qc); verr != nil {
		log.Debug("hotstuff: header-QC canonicalize skipped (verify failed)",
			"qcBlock", qc.BlockHash, "qcView", qc.View, "err", verr)
		return
	}
	if cerr := s.blockProducer.CommitToCanonical(qc.BlockHash); cerr != nil {
		log.Debug("hotstuff: header-QC commit-to-canonical deferred", "hash", qc.BlockHash, "err", cerr)
	} else {
		log.Info("hotstuff: canonical advanced from header-embedded CommitQC",
			"number", *qcNum, "hash", qc.BlockHash)
	}
}

// maxNotifiedImports bounds the NotifyBlockImported dedup set (see notifiedImports).
// It MUST stay below the engine's MaxImportedBlocks (64): the sync dedup must
// forget a hash BEFORE the engine forgets it, so that if a long-uncommitted
// block is somehow re-proposed after the engine evicted it, the next re-gossip
// re-notifies and re-populates the engine instead of being deduped into a stall.
const maxNotifiedImports = 32
