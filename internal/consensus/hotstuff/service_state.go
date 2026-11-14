// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service vote journal and persisted consensus state recovery.

package hotstuff

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var journalTrace = envswitch.Is1("N42_JOURNAL_TRACE", envswitch.Diag, "Trace the vote journal.")

// JournalVote implements VoteJournal: it makes a vote commitment durable before
// the engine releases the vote. Called on the ENGINE goroutine with the engine
// mutex held, so it must not touch any locked engine accessor — everything it
// needs is already in state.
func (s *Service) JournalVote(state *ConsensusState) error {
	// Store priority: the independent vote store (vote_store.go, opt-in via
	// N42_SEPARATE_VOTE_DB and sticky through its marker) also carries the
	// vote epoch; otherwise the separate journal store (vote_journal_db.go);
	// otherwise the chain database.
	db, store := s.db, "main"
	if s.journalDB != nil {
		db, store = s.journalDB, "journal"
	}
	if s.voteDB != nil {
		db, store = s.voteDB, "separate"
	}
	if db == nil {
		return nil // no store configured (tests/embedded): journalling is a no-op
	}
	write := func(tx kv.RwTx) error {
		if err := SaveConsensusState(tx, state); err != nil {
			return err
		}
		if s.voteDB != nil {
			return saveVoteEpoch(tx, state.VoteEpoch)
		}
		return nil
	}
	if !journalTrace {
		return db.Update(s.ctx, write)
	}
	start := time.Now()
	var acquire, save time.Duration
	err := db.Update(s.ctx, func(tx kv.RwTx) error {
		acquire = time.Since(start)
		t := time.Now()
		err := write(tx)
		save = time.Since(t)
		return err
	})
	log.Info("hotstuff: vote journal phases", "store", store, "view", state.View,
		"prepareView", state.LastVotedView, "commitView", state.LastCommitVotedView,
		"prepareHash", state.LastVotedHash.Hex(), "commitHash", state.LastCommitVotedHash.Hex(),
		"acquire", acquire, "save", save, "total", time.Since(start), "err", err)
	return err
}

// persistState saves the current consensus state to the database using the
// service context.
func (s *Service) persistState() { s.persistStateCtx(s.ctx) }

// persistStateCtx is persistState with an explicit context. Shutdown must pass
// a live one: MDBX refuses to open a write transaction on a cancelled context,
// so the "final state persist" in Stop — which runs AFTER s.cancel() — could
// never actually write anything. Every graceful stop therefore left the node
// with whatever the last commit-time persist had stored, which is precisely how
// a restart comes back on a view/lock that no longer matches the applied chain.
// stateHook carries a resolved consensus-state snapshot and the closure that
// writes it. Both engine accessors are resolved when the hook is BUILT, never
// inside the transaction: calling a locked engine accessor while holding the
// MDBX writer lets this goroutine wait on the engine mutex whose holder is
// waiting for that same writer inside JournalVote -- a deadlock that stops the
// node dead.
type stateHook struct {
	run  func(tx kv.RwTx) error
	view uint64
	done bool // set by run on success; read only after the transaction returns
}

// stateHookCommitted reports whether the snapshot reached durable storage in
// the enclosing canonicalization transaction. done only proves the callback
// completed; the outer error proves the subsequent transaction commit did too.
func stateHookCommitted(h *stateHook, commitErr error) bool {
	return h != nil && h.done && commitErr == nil
}

// newStateHook resolves the state to persist. Returns nil when there is
// nothing to persist (no store, no engine).
func (s *Service) newStateHook() *stateHook {
	if s.db == nil {
		return nil
	}
	ce := s.engine.Engine()
	if ce == nil {
		return nil
	}
	// One locked snapshot: view, lock, last-committed QC AND the vote
	// commitments. Reading them field by field could store a lock from one view
	// with a vote from the next -- and omitting the vote fields entirely would
	// CLOBBER the journalled vote history with zeros on the next periodic
	// persist, silently re-opening the equivocation window the journal exists
	// to close.
	state := ce.SnapshotState()
	stagedEpoch, stagedVals, stagedF, hasStaged := ce.StagedEpochInfoSafe()
	h := &stateHook{view: state.View}
	h.run = func(tx kv.RwTx) error {
		if err := SaveConsensusState(tx, state); err != nil {
			return err
		}
		if hasStaged {
			// Staged epoch data rides the same transaction so a crash cannot
			// separate it from the state that refers to it.
			if err := SaveStagedEpoch(tx, stagedEpoch, stagedVals, stagedF); err != nil {
				return err
			}
		}
		h.done = true
		return nil
	}
	return h
}

func (s *Service) persistStateCtx(ctx context.Context) {
	h := s.newStateHook()
	if h == nil {
		return
	}
	if err := s.db.Update(ctx, h.run); err != nil {
		log.Warn("hotstuff: failed to persist state", "err", err)
		return
	}
	s.setLastPersistedView(h.view)
}

// recoverEpochState restores the persisted validator-set state after a restart.
// Reconfiguration lives only in memory, so without this a restarted node reverts to
// the genesis set and can no longer verify QCs signed by a reconfigured set — which
// stalls the chain (a "signers bitmap length mismatch" livelock, reproducible by
// crashing a node after a validator add/remove). Restores the ACTIVE set, and any
// committed-but-unactivated STAGED set, before consensus resumes.
func (s *Service) recoverEpochState() error {
	if s.db == nil {
		return nil
	}
	ce := s.engine.Engine()
	if ce == nil {
		return nil
	}
	var (
		aEpoch uint64
		aVals  []ValidatorInfo
		aF     uint32
		aOk    bool
		sVals  []ValidatorInfo
		sF     uint32
	)
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		var e1 error
		aEpoch, aVals, aF, aOk, e1 = LoadActiveEpoch(tx)
		if e1 != nil {
			return e1
		}
		_, sVals, sF, e1 = LoadStagedEpoch(tx)
		return e1
	}); err != nil {
		return fmt.Errorf("load persisted epoch state: %w", err)
	}
	if aOk && len(aVals) > 0 {
		ce.RestoreValidatorSet(aEpoch, aVals, aF)
	}
	if len(sVals) > 0 {
		ce.RestoreStagedSet(sVals, sF)
	}
	return nil
}

// recoverState loads persisted consensus state and reinitializes the engine.
func (s *Service) recoverState() error {
	if err := s.recoverEpochState(); err != nil {
		return err
	}

	var state *ConsensusState
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		var err error
		state, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		return err
	}
	state, err := s.mergeVoteJournal(state)
	if err != nil {
		return err
	}
	if state == nil {
		state = &ConsensusState{LockedQC: GenesisQC(), LastCommittedQC: GenesisQC()}
	}
	if state.LockedQC.View > state.View || state.LastCommittedQC.View > state.View {
		return fmt.Errorf("corrupted consensus state: QC view exceeds state view %d", state.View)
	}
	// The active epoch may have reached disk before its round checkpoint.
	// Never seed an already-restored validator set back into an earlier epoch.
	ce := s.engine.Engine()
	if ce != nil {
		epoch, length := ce.epochManager.CurrentEpoch(), ce.epochManager.epochLength
		if epoch > 0 && length > 0 {
			if epoch > (math.MaxUint64-1)/length {
				return fmt.Errorf("persisted epoch boundary overflows view")
			}
			if boundary := epoch*length + 1; state.View < boundary {
				state.View = boundary
			}
		}
	}

	log.Info("hotstuff: recovering persisted state",
		"view", state.View, "timeouts", state.ConsecutiveTimeouts,
		"lockedQCView", state.LockedQC.View, "committedQCView", state.LastCommittedQC.View,
		"lastVotedView", state.LastVotedView, "lastCommitVotedView", state.LastCommitVotedView)

	// The engine was already created by adapter.New(). We need to reinitialize
	// with recovered state if the persisted view is ahead.
	if ce == nil {
		return nil
	}
	currentView := ce.CurrentView()
	if state.View > currentView {
		ce.RestoreState(state.View, state.LockedQC, state.LastCommittedQC, state.ConsecutiveTimeouts)
		log.Info("hotstuff: engine restored to persisted state", "view", state.View)
	}

	// Vote commitments are restored UNCONDITIONALLY — including when the
	// persisted view was not ahead. They are the double-vote guard: skipping
	// them would let this node cast a second, conflicting vote in a view it
	// already voted in before the restart, which its own equivocation detector
	// treats as a slashable offence.
	ce.RestoreVoteCommitments(state.LastVotedView, state.LastVotedHash,
		state.LastCommitVotedView, state.LastCommitVotedHash)

	switch s.reportPersistedStateDivergence(state) {
	case persistedStateAhead, persistedStateBlockUnknown:
		// A genuine committed QC this node's canonical chain has not reached
		// (the commit deferred at shutdown or crash): commit it once the
		// block is applied here, the same as the in-run retry.
		s.pendingMu.Lock()
		s.recoveredCommit = state.LastCommittedQC.BlockHash
		s.pendingMu.Unlock()
	}

	return nil
}

// persistedStateStatus classifies the recovered consensus state against the
// locally applied chain.
type persistedStateStatus uint8

const (
	persistedStateUncheckable  persistedStateStatus = iota // no store, genesis QC, or read failure
	persistedStateConsistent                               // committed QC block is canonical locally
	persistedStateBlockUnknown                             // committed QC names a block we do not have yet
	persistedStateDiverged                                 // committed QC names a block that is NOT canonical here
	persistedStateAhead                                    // committed QC block descends from, and is above, the canonical head
)

// reportPersistedStateDivergence checks the recovered consensus state against
// the locally applied chain and reports a mismatch LOUDLY.
//
// A persisted LastCommittedQC naming a block that is not on this node's
// canonical chain is the signature of the wedge that took the whole fleet down:
// consensus resumes locked on a branch the node never applied, every
// block-production attempt then fails AlignAppliedBranch with "would revert
// committed block", and the only symptom used to be a miner runLoop error while
// the consensus layer said nothing at all. Read-only and best effort — it never
// changes state, because silently rewriting a safety lock is far more dangerous
// than a stalled node.
func (s *Service) reportPersistedStateDivergence(state *ConsensusState) persistedStateStatus {
	if s.db == nil || state == nil {
		return persistedStateUncheckable
	}
	hash := state.LastCommittedQC.BlockHash
	if hash == (types.Hash{}) {
		return persistedStateUncheckable // genesis QC: nothing to compare against
	}

	var (
		known       bool
		ahead       bool
		number      uint64
		canonical   types.Hash
		canonHeight uint64
	)
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		if hdr, err := rawdb.ReadHeaderByHash(tx, hash); err == nil && hdr != nil && hdr.Number != nil {
			known, number = true, hdr.Number.Uint64()
		}
		if cur := rawdb.ReadCurrentHeader(tx); cur != nil && cur.Number != nil {
			canonHeight = cur.Number.Uint64()
		}
		if known {
			c, err := rawdb.ReadCanonicalHash(tx, number)
			if err != nil {
				return err
			}
			canonical = c
			if c != hash {
				a, _, err := CommittedQCAheadOfCanonical(tx, hash)
				if err != nil {
					return err
				}
				ahead = a
			}
		}
		return nil
	}); err != nil {
		log.Warn("hotstuff: could not cross-check persisted state against local chain", "err", err)
		return persistedStateUncheckable
	}

	switch {
	case !known:
		// The block may still arrive by sync, so this alone is not fatal.
		log.Warn("hotstuff: persisted committed QC names a block this node does not have",
			"blockHash", hash, "qcView", state.LastCommittedQC.View, "localHead", canonHeight)
		return persistedStateBlockUnknown
	case canonical != hash && ahead:
		log.Warn("hotstuff: persisted committed QC is ahead of the canonical chain (commit deferred at shutdown); "+
			"it is canonicalized once its block is applied here",
			"committedQCBlock", hash, "committedQCView", state.LastCommittedQC.View,
			"height", number, "localHead", canonHeight)
		return persistedStateAhead
	case canonical != hash:
		log.Error("hotstuff: PERSISTED CONSENSUS STATE HAS DIVERGED FROM THE APPLIED CHAIN — "+
			"the last committed QC names a block that is not canonical locally. Consensus will "+
			"resume locked on a branch this node never applied, block production will fail with "+
			"'would revert committed block', and the node will stop producing until this is "+
			"resolved. Operator action: stop the node and clear the persisted consensus state "+
			"with `go run ./cmd/hotstuff-reset -datadir <datadir>/chaindata -apply` "+
			"(chain data is untouched; consensus restarts from the applied head).",
			"committedQCBlock", hash, "committedQCView", state.LastCommittedQC.View,
			"heightOfThatBlock", number, "canonicalAtThatHeight", canonical,
			"localHead", canonHeight, "lockedQCBlock", state.LockedQC.BlockHash,
			"lockedQCView", state.LockedQC.View)
		metricPersistedStateDiverged.Inc()
		return persistedStateDiverged
	default:
		log.Info("hotstuff: persisted consensus state agrees with the applied chain",
			"committedQCBlock", hash, "height", number, "localHead", canonHeight)
		return persistedStateConsistent
	}
}
