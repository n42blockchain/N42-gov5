// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Service block-production pacing: execution lag gate, deferral and retry.

package hotstuff

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// blockProductionSyncGate is the height lag (in blocks) above which a leader
// pauses block production and catches up first. A small allowance absorbs the
// transient one-block lag of normal operation (a leader that just committed a
// height its peers already have) without halting production, while still
// catching the startup self-fork case where a node trails by many blocks.
const blockProductionSyncGate = 2

// committedUnexecutedRetryLimit is the number of consecutive local execution
// checks that may fail before leaders refuse to extend an unavailable parent.
// The streak deliberately spans distinct commit hashes: a lagging node sees a
// new CommitQC every view, so resetting on hash change would keep the guard at
// one forever. A short grace period still covers normal block-arrival skew.
const committedUnexecutedRetryLimit = 3

// heightBehind returns how far the local head trails the network, or 0 when no
// block fetcher is wired (preserving unconditional production for chains without
// the sync layer).
func (s *Service) heightBehind() uint64 {
	if s.blockFetcher == nil {
		return 0
	}
	return s.blockFetcher.HeightBehind()
}

// blockExecutionStatus checks the applied-lineage evidence exposed by the sync
// layer. The bool result is false only when the probe is unavailable; a missing
// local header is a checked, unexecuted result.
func (s *Service) blockExecutionStatus(hash types.Hash) (applied, checked bool, number uint64) {
	probe, ok := s.blockFetcher.(interface {
		BlockApplied(types.Hash, uint64) bool
	})
	if !ok {
		return false, false, 0
	}
	if s.db == nil {
		return false, true, 0
	}
	haveHeader := false
	_ = s.db.View(s.ctx, func(tx kv.Tx) error {
		if hdr, err := rawdb.ReadHeaderByHash(tx, hash); err == nil && hdr != nil && hdr.Number != nil {
			number = hdr.Number.Uint64()
			haveHeader = true
		}
		return nil
	})
	if !haveHeader {
		return false, true, 0
	}
	return probe.BlockApplied(hash, number), true, number
}

// observeCommittedExecution records high-visibility evidence that a committed
// block has not executed locally. countMetric is true only for the CommitQC
// event; retry observations update the consecutive-failure guard without
// inflating the event counter.
func (s *Service) observeCommittedExecution(hash types.Hash, countMetric bool) (bool, uint8, uint64) {
	applied, checked, number := s.blockExecutionStatus(hash)
	if !checked {
		return true, 0, number // compatibility for deployments without the applied probe
	}
	s.pendingMu.Lock()
	if applied {
		s.unexecutedCommitHash = types.Hash{}
		s.unexecutedCommitFailures = 0
		s.pendingMu.Unlock()
		return true, 0, number
	}
	// Do not reset the streak when the commit hash changes. A node whose
	// execution head is falling behind observes a different missing committed
	// block every view; per-hash counting never reaches the production gate.
	s.unexecutedCommitHash = hash
	if s.unexecutedCommitFailures < ^uint8(0) {
		s.unexecutedCommitFailures++
	}
	failures := s.unexecutedCommitFailures
	s.pendingMu.Unlock()

	if countMetric {
		metricCommittedUnexecuted.Inc()
	}
	log.Error("hotstuff: committed block not executed locally",
		"hash", hash, "number", number, "failures", failures)
	return false, failures, number
}

// requestCommittedCatchUp uses the CommitQC-authenticated block number as a
// catch-up target. Peer status heights are refreshed much less frequently than
// HotStuff commits, so relying only on advertised height can leave a restarted
// validator permanently chasing a stale target.
func (s *Service) requestCommittedCatchUp(hash types.Hash, number uint64) {
	if s.blockFetcher == nil {
		return
	}
	targeted, hasTargetedCatchUp := s.blockFetcher.(interface{ CatchUpTo(uint64) })
	if hasTargetedCatchUp && number > 0 {
		go targeted.CatchUpTo(number)
		return
	}
	if byHash, ok := s.blockFetcher.(interface{ CatchUpToHash(types.Hash) }); ok {
		byHash.CatchUpToHash(hash)
		return
	}
	s.blockFetcher.FetchBlockByHash(hash)
	if !hasTargetedCatchUp {
		return
	}

	// The CommitQC can beat the block body to this node, so the first probe has
	// no header number. Fetch the authenticated hash, then briefly wait for its
	// header to land and convert it into a range target. Without this bridge a
	// restarted node falls back to one-parent-at-a-time fetches and cannot catch
	// a chain that is producing faster than that retry loop.
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				_, checked, observedNumber := s.blockExecutionStatus(hash)
				if checked && observedNumber > 0 {
					targeted.CatchUpTo(observedNumber)
					return
				}
			}
		}
	}()
}

// deferProduction parks a leader view whose parent is not applied yet so
// NotifyBlockImported re-runs the gate when it lands. The import can complete
// between the gate's check and this registration; a second, side-effect-free
// probe after registering closes that window by re-running the gate at once.
func (s *Service) deferProduction(view ViewNumber, parentHash types.Hash) {
	s.pendingMu.Lock()
	s.deferredProduce.view, s.deferredProduce.parent = view, parentHash
	s.pendingMu.Unlock()
	if applied, checked, _ := s.blockExecutionStatus(parentHash); checked && applied {
		s.pendingMu.Lock()
		raced := s.deferredProduce.parent == parentHash
		if raced {
			s.deferredProduce.parent = types.Hash{}
		}
		s.pendingMu.Unlock()
		if raced {
			log.Info("hotstuff: deferred production resumed at once, the parent applied during the gate", "view", uint64(view), "parent", parentHash.Hex()[:12])
			go s.triggerBlockProduction(view, parentHash)
		}
	}
}

// SetLagGate attaches the leader gate of deferred depth k > 1 (P2a):
// ready once this node executed the block a child of parent must report;
// waitFor names that block. It replaces the committed-parent and
// applied-parent gates, which a leader building without execution does not
// need.
func (s *Service) SetLagGate(fn func(parent types.Hash) (ready bool, waitFor types.Hash, err error)) {
	s.lagGate = fn
}

// deferProductionUntil parks a leader view until waitFor is imported (or,
// for a parent this node does not hold yet, checked); the view then re-runs
// the gate. As deferProduction, a second probe after registering closes the
// window where the import landed in between.
func (s *Service) deferProductionUntil(view ViewNumber, parentHash, waitFor types.Hash) {
	s.pendingMu.Lock()
	s.deferredProduce.view, s.deferredProduce.parent, s.deferredProduce.waitFor = view, parentHash, waitFor
	s.deferredProduce.lagParked = true
	s.pendingMu.Unlock()
	if s.lagGate == nil {
		return
	}
	if ready, _, err := s.lagGate(parentHash); err == nil && ready {
		s.pendingMu.Lock()
		raced := s.deferredProduce.lagParked && s.deferredProduce.parent == parentHash && s.deferredProduce.view == view
		if raced {
			s.deferredProduce.parent, s.deferredProduce.waitFor, s.deferredProduce.lagParked = types.Hash{}, types.Hash{}, false
		}
		s.pendingMu.Unlock()
		if raced {
			go s.triggerBlockProduction(view, parentHash)
		}
	}
}

// resumeDeferredProduction re-runs a parked leader view when hash is what
// it waits for (its parent, or under the lag gate the block it must have
// executed).
func (s *Service) resumeDeferredProduction(hash types.Hash, why string) {
	if hash == (types.Hash{}) {
		return
	}
	s.pendingMu.Lock()
	// A lag-parked view may extend the zero parent (no LockedQC block yet:
	// the canonical head), so it is matched on its own flag.
	match := s.deferredProduce.parent == hash || (s.deferredProduce.lagParked && s.deferredProduce.waitFor == hash)
	view, parent := s.deferredProduce.view, s.deferredProduce.parent
	if match {
		s.deferredProduce.parent, s.deferredProduce.waitFor, s.deferredProduce.lagParked = types.Hash{}, types.Hash{}, false
	}
	s.pendingMu.Unlock()
	if !match || s.engine == nil {
		return
	}
	if eng := s.engine.Engine(); eng != nil && eng.IsCurrentLeader() && eng.CurrentView() == view {
		log.Info("hotstuff: deferred production resumed", "view", uint64(view), "parent", parent.Hex()[:12], "after", why, "hash", hash.Hex()[:12])
		go s.triggerBlockProduction(view, parent)
	}
}

// committedParentBlocked rechecks the proposed consensus parent and fails
// closed after repeated local execution failures. The failure streak is
// chain-wide rather than keyed by hash because commits keep advancing while a
// restarted node's execution head is stalled.
func (s *Service) committedParentBlocked(parentHash types.Hash) bool {
	s.pendingMu.Lock()
	hasExecutionGap := s.unexecutedCommitFailures > 0 && parentHash != (types.Hash{})
	s.pendingMu.Unlock()
	if !hasExecutionGap {
		return false
	}
	applied, failures, number := s.observeCommittedExecution(parentHash, false)
	if applied || failures < committedUnexecutedRetryLimit {
		return false
	}
	s.requestCommittedCatchUp(parentHash, number)
	log.Error("hotstuff: refusing block production on unexecuted committed parent",
		"hash", parentHash, "number", number, "failures", failures)
	return true
}

// ensureParentApplied guarantees the block a leader is about to extend is the
// live applied head, and re-applies it when the applied head trails it.
//
// Two recovery paths run at startup and they can disagree. The execution layer
// reverts speculative (uncommitted) blocks back to the last COMMITTED head
// (BlockChain.revertSpeculativeOnStartup); consensus restores its LockedQC from
// disk, and that lock may name exactly the uncommitted block the revert just
// rolled back. Every leader then extends the locked block,
// checkQMDBLeaderSealParent finds a parent that is no longer the applied head,
// and the sealed block is dropped as ErrStaleSeal. Nothing is broadcast, the
// view times out, the next leader repeats it — the fleet wedges while the
// loudest symptom is an Info line saying a seal lost to a competing candidate.
//
// Observed live 2026-08-21: seven validators stopped together at applied
// 14527898 / committed 14527897 and then spun through 13 views producing
// nothing. A single restarting node self-heals (its peers keep committing and
// catch-up re-imports the block); it is the fleet-wide restart, where nobody
// can make progress, that needs this.
//
// Re-applying goes through the sync layer's fetch-on-miss, which already knows
// how to walk back to an applied ancestor and replay forward, and fetches the
// body from a peer when this node lacks it. That is asynchronous; a completed
// import wakes the output loop to retry if the same leader view is still valid.
//
// Returns true when production may proceed.
func (s *Service) ensureParentApplied(parentHash types.Hash) bool {
	if parentHash == (types.Hash{}) || s.db == nil || s.blockFetcher == nil {
		return true
	}
	var (
		appliedNum  uint64
		appliedHash types.Hash
		haveApplied bool
		parentNum   uint64
		haveParent  bool
	)
	if err := s.db.View(s.ctx, func(tx kv.Tx) error {
		// Read the SAME marker checkQMDBLeaderSealParent rejects against, so
		// this gate and the failure it prevents cannot drift apart.
		if n, h, ok, rerr := rawdb.ReadQMDBApplied(tx); rerr == nil && ok {
			appliedNum, appliedHash, haveApplied = n, types.Hash(h), true
		}
		if hdr, rerr := rawdb.ReadHeaderByHash(tx, parentHash); rerr == nil && hdr != nil && hdr.Number != nil {
			parentNum, haveParent = hdr.Number.Uint64(), true
		}
		return nil
	}); err != nil {
		return true // never block production on a failed diagnostic read
	}
	// No marker means no speculative-apply tracking (non-QMDB commitment), and
	// then there is no stale-seal rejection to pre-empt either.
	if !haveApplied || appliedHash == parentHash {
		return true
	}
	// Only "applied trails the lock" is ours. A higher applied head on a
	// sibling branch is an ordinary branch switch, which the miner's own align
	// phase owns — it can unwind, which is what that case needs. Replaying
	// forward is the thing it cannot do, and the thing this path supplies.
	if haveParent && appliedNum >= parentNum {
		return true
	}
	log.Warn("hotstuff: consensus parent is not the applied head; re-applying before producing",
		"parent", parentHash, "parentNumber", parentNum, "haveParentHeader", haveParent,
		"appliedNumber", appliedNum, "appliedHash", appliedHash)
	metricParentReapplied.Inc()
	s.blockFetcher.FetchBlockByHash(parentHash)
	return false
}

// triggerBlockProduction runs three gates before handing the build to the miner.
// Each is timed: the leader's ViewStart -> ProposalSent stage measures 615 ms
// under load while the miner's own phase timers account for only ~262 ms of it,
// so the missing time is either in these gates (ensureParentApplied opens a read
// transaction on every view) or in the wait for the single worker goroutine,
// which "miner: work queue wait" reports from the other side. Observability
// only -- no gate is skipped or reordered.
func (s *Service) triggerBlockProduction(view ViewNumber, parentHash types.Hash) {
	s.pendingProduction = &productionRequest{view: view, parent: parentHash}
	tGate := time.Now()
	var dBehind, dCommitted, dApplied time.Duration
	logGates := func(outcome string) {
		log.Info("hotstuff: leader gate phases", "view", uint64(view), "outcome", outcome,
			"behindNs", dBehind.Nanoseconds(), "committedNs", dCommitted.Nanoseconds(),
			"appliedNs", dApplied.Nanoseconds(), "totalNs", time.Since(tGate).Nanoseconds())
	}
	behind := s.heightBehind()
	if behind > blockProductionSyncGate {
		dBehind = time.Since(tGate)
		logGates("behind")
		log.Warn("hotstuff: behind peers, skipping block production and catching up",
			"view", view, "behind", behind, "gate", blockProductionSyncGate)
		if s.blockFetcher != nil {
			go s.blockFetcher.CatchUp()
		}
		return
	}
	dBehind = time.Since(tGate)
	if parentHash == (types.Hash{}) && behind > 0 {
		// No lock (fresh or reset consensus state): the producer would build
		// on this node's own committed head. A peer ahead of it may have
		// committed the block above that head, and a proposal here is then a
		// sibling of that committed block, which those peers refuse to vote
		// for -- round S131: nodes 0-3, one block short after a reset,
		// proposed sibling 88e236 of f343 that nodes 4-6 had committed. Catch
		// up instead; a validator at the top height leads a later view and
		// its child of the higher block extends every node's committed head.
		logGates("behind-unlocked")
		log.Warn("hotstuff: no consensus lock and behind peers, catching up instead of proposing on the local head",
			"view", view, "behind", behind)
		if s.blockFetcher != nil {
			go s.blockFetcher.CatchUp()
		}
		return
	}
	if parentHash != (types.Hash{}) && s.engine != nil {
		if ce := s.engine.Engine(); ce != nil && ce.ConflictsWithCommitted(parentHash) {
			// The lock names a block off this node's committed chain; the
			// proposal (which the leader also votes for) would conflict with
			// it. Peers' timeouts carry a higher QC that replaces the lock.
			logGates("parent-conflicts-committed")
			log.Error("hotstuff: consensus parent conflicts with this node's committed block, not producing",
				"view", view, "parent", parentHash)
			return
		}
	}
	if s.lagGate != nil {
		// The lag gate resumes a parked view through deferProductionUntil,
		// never through the import-driven retry: the retry request set above
		// stays "current" (same view, same lock) from the seal until the QC,
		// so the leader's own applied block would rebuild the same parent --
		// a same-height sibling per proposal (r60a, bench run 21).
		s.pendingProduction = nil
		// P2a, deferred depth k > 1: the leader builds without executing,
		// so the parent need not be applied; it needs this node's result of
		// the block k-1 above the parent (N-k for height N), which its
		// header carries. Wait for that block's execution instead.
		tA := time.Now()
		ready, waitFor, err := s.lagGate(parentHash)
		dApplied = time.Since(tA)
		if err == nil && !ready {
			logGates("lag-wait")
			log.Info("hotstuff: leader waits for its lagged execution", "view", uint64(view),
				"parent", parentHash.Hex()[:12], "waitFor", waitFor.Hex()[:12])
			if waitFor == parentHash && parentHash != (types.Hash{}) && s.blockFetcher != nil && !s.availableHere(parentHash) {
				s.blockFetcher.FetchBlockByHash(parentHash)
			}
			s.deferProductionUntil(view, parentHash, waitFor)
			return
		}
		logGates("trigger")
		s.blockProducer.TriggerBlockProduction(parentHash)
		return
	}
	tC := time.Now()
	if s.committedParentBlocked(parentHash) {
		dCommitted = time.Since(tC)
		logGates("committed-parent-blocked")
		// The guard fires on a chain-wide streak of commits that landed
		// before their block was executed here -- routine at 163k a block,
		// where the commit QC outruns the follower's 1.5 s import -- and the
		// parent is then one import away: round 35ze B1, both timeouts were
		// this gate at a tenure handoff, the parent applied 100 ms later, and
		// nothing resumed the view because only the parent-not-applied
		// branch registered a retry. Register it here too.
		return
	}
	dCommitted = time.Since(tC)
	tA := time.Now()
	if !s.ensureParentApplied(parentHash) {
		dApplied = time.Since(tA)
		logGates("parent-not-applied")
		return
	}
	dApplied = time.Since(tA)
	logGates("trigger")
	s.pendingProduction = nil
	s.blockProducer.TriggerBlockProduction(parentHash)
}

type productionRequest struct {
	view   ViewNumber
	parent types.Hash
}

// retryBlockProduction runs only on the output loop. Import notifications are
// coalesced wakeups, not authorization to build: the pending view, leader and
// lock must still match, and triggerBlockProduction repeats every sync gate.
func (s *Service) retryBlockProduction() {
	req := s.pendingProduction
	if req == nil || s.blockProducer == nil {
		return
	}
	ce := s.engine.Engine()
	ce.mu.Lock()
	current := !ce.removed && ce.isMember() && ce.roundState.CurrentView() == req.view &&
		ce.roundState.LockedQC().BlockHash == req.parent && ce.isLeader(ce.myIndex, req.view, ce.validatorSet())
	ce.mu.Unlock()
	if !current || s.ctx.Err() != nil {
		s.pendingProduction = nil
		return
	}
	s.triggerBlockProduction(req.view, req.parent)
}
