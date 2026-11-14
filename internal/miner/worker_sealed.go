// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Sealed-block handling: resultLoop, handleSealed, writeAndFinish and seal bookkeeping.

package miner

import (
	"bytes"
	"errors"
	"runtime/debug"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

func (w *worker) resultLoop() error {
	defer w.cancel()
	defer w.stop()

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case blk := <-w.resultCh:
			w.handleSealed(blk)
		}
	}
}

// handleSealed persists, pushes and proposes one sealed block. It runs under
// its own recover so a panic on one block costs that block, not the worker:
// resultLoop stops the worker when it returns, so a panic that escaped from
// here left the node silently skipping every leader view for the life of the
// process (round 35r: node3 lost its miner at 12:08 and missed 18 views of one
// leg, each a 6 s view timeout).
func (w *worker) handleSealed(blk block.IBlock) {
	// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct): this is resultLoop's own
	// entry for this sealed result -- as early in this function as possible,
	// right after the two constant-time guards above, so it is a close proxy
	// for "resultLoop received this from resultCh". Used below (once the
	// pending task is found) to derive resQWaitMs: the gap between Seal()
	// itself finishing (task.sealStart + task.blsNanos) and this stamp is the
	// time this exact sealed block spent waiting for resultLoop to be free --
	// i.e. queued behind whatever handleSealed(v) is still doing (its own
	// WriteBlockWithState, most likely) on this SAME single goroutine.
	var tHandleSealedEnter time.Time
	if contentionDiagEnabled {
		tHandleSealedEnter = time.Now()
	}
	defer func() {
		if r := recover(); r != nil {
			id := "nil"
			if blk != nil {
				id = blk.Hash().Hex()[:12]
			}
			log.Errorf("panic handling sealed block %s; dropping it: %v\nstack: %s", id, r, debug.Stack())
			miningErrorsCounter.Inc()
		}
	}()
	if blk == nil {
		return
	}
	blockNumber, err := requireBlockNumber(blk, "block number unavailable")
	if err != nil {
		log.Error("Ignoring sealed block", "err", err, "hash", blk.Hash())
		return
	}

	// Short circuit when receiving duplicate result caused by resubmitting.
	// Under lag mode an identical rebuild may be held but not yet written.
	known := w.chain.HasBlock(blk.Hash(), blockNumber.Uint64())
	if !known && w.lagBuildEnabled() {
		if bc, ok := w.chain.(*internal.BlockChain); ok && bc.AvailableBlock(blk.Hash()) != nil {
			known = true
		}
	}
	if known {
		if usesTimerDrivenSealing(w.engine) {
			return
		}
		if sl, ok := w.chain.(siblingLookup); ok && sl.BadSibling(blk.Hash()) {
			log.Warn("miner: deterministic rebuild equals a block this node failed to validate; not re-proposing it",
				"number", blockNumber.Uint64(), "hash", blk.Hash().Hex()[:12])
			return
		}
		// Leader-driven (HotStuff): the deterministic rebuild collides
		// with a block already imported in an EARLIER round (restart at a
		// stalled height re-enters overlapping views, so the identical
		// candidate is already in the DB — imported but never committed).
		// It still must be proposed for THIS view: skip the state
		// re-write, but re-push and notify so the Proposal goes out.
		log.Info("miner: re-proposing already-imported block", "number", blockNumber.Uint64(), "hash", blk.Hash().Hex()[:12])
		if err := w.chain.SealedBlock(blk); err != nil {
			log.Warn("miner: re-push of existing block failed", "err", err)
		}
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
		}
		return
	}

	parentHash := blk.ParentHash()
	w.rememberSealed(blk)

	// Fix A — cross-view same-height convergence: if a strictly-lower-hash
	// sibling extending THIS parent is already known locally (sealed here or
	// received from another view's leader), re-propose it instead of
	// importing this divergent higher-hash candidate. When a height misses
	// its first commit, each later view's leader otherwise builds a distinct
	// block (different ConsensusEvidence per view), scattering import-gated
	// votes so no candidate reaches the 2f+1 quorum — the multi-minute
	// convergence stall. Deterministic lowest-hash selection (matches the
	// forkchoice tie-break) makes every leader pick the SAME candidate, so
	// votes stack on one block. See docs/hotstuff-view-convergence-followup.md.
	// Only at the single legitimate proposal height (committed head + 1):
	// a stale leader building at an already-committed height must NOT be
	// redirected to a dead sibling there — that re-injects a candidate
	// conflicting with the committed block (observed live at 13014242:
	// sibling re-proposal at a committed height seeded a canonical-chain
	// discontinuity). Let the normal import/duplicate path absorb it.
	if sl, ok := w.chain.(siblingLookup); ok &&
		blockNumber.Uint64() == w.chain.CurrentBlock().Number64().Uint64()+1 {
		if low, exists := sl.LowestSiblingAtHeight(blockNumber.Uint64(), parentHash); exists &&
			low.Hash() != blk.Hash() &&
			bytes.Compare(low.Hash().Bytes(), blk.Hash().Bytes()) < 0 {
			log.Info("miner: converging on lowest-hash sibling; re-proposing it",
				"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
				"kept", low.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12])
			w.releaseLostFrames(blk, low)
			if err := w.chain.SealedBlock(low); err != nil {
				log.Warn("miner: re-push of lowest sibling failed", "err", err)
			}
			if bsn, ok := w.engine.(blockSealNotifier); ok {
				bsn.NotifyBlockSealed(low.Hash(), low.TxHash())
			}
			return
		}
	}

	// Height-level single-candidate guard (HotStuff-2): keep only the first
	// block sealed on a given parent. A later view's leader re-entering Seal
	// on the same still-uncommitted parent produces a divergent sibling;
	// importing it would fork the applied state and thrash the branch-switch
	// unwind between the two siblings forever. Re-propose the kept block for
	// THIS view instead — it is already imported and is what consensus is
	// building on. Checked BEFORE import so the sibling never touches state.
	if kept := w.firstSealedOnParent(parentHash); kept != nil && kept.Hash() != blk.Hash() {
		// S34 (docs/OPEN_ISSUES.md, the stale-re-proposal item;
		// docs/QS_BLOCK_TIME_BUDGET.md 6dk): "kept" is only ever the FIRST
		// block sealed on parentHash -- it says nothing about whether that
		// height has since been committed and written. Round 35zzzk view
		// 6557: a leftover handleSealed call for a divergent sibling at the
		// height that had JUST been committed (parentHash was the tip BEFORE
		// that commit) found "kept" already equal to the just-committed
		// block itself and re-proposed it -- an already-decided block,
		// re-injected as if it were a fresh candidate, which the engine's
		// own self-justify guard (proposal.go) now also refuses. This check
		// avoids even attempting it: if the chain's own current head is
		// already at or past this height, the height was decided while this
		// stale task was in flight, so nothing here is worth proposing.
		// Best-effort, not the safety net -- CurrentBlock() reflects a
		// completed WRITE, which can in principle still be in flight when
		// this runs; the engine's own unconditional guard is what actually
		// closes the defect regardless of this check's timing.
		if w.chain.CurrentBlock().Number64().Uint64() >= blockNumber.Uint64() {
			log.Info("miner: not re-proposing a sibling at an already-decided height",
				"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
				"kept", kept.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12],
				"currentBlock", w.chain.CurrentBlock().Number64().Uint64())
			w.releaseLostFrames(blk, kept)
			return
		}
		log.Info("miner: suppressing divergent same-height sibling; re-proposing first sealed block",
			"number", blockNumber.Uint64(), "parent", parentHash.Hex()[:12],
			"kept", kept.Hash().Hex()[:12], "dropped", blk.Hash().Hex()[:12])
		w.releaseLostFrames(blk, kept)
		if err := w.chain.SealedBlock(kept); err != nil {
			log.Warn("miner: re-push of kept sibling failed", "err", err)
		}
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(kept.Hash(), kept.TxHash())
		}
		return
	}

	// S26 (docs/OPEN_ISSUES.md "A quorum-committed block that no node
	// stored"): record THIS block as the sole candidate for parentHash now,
	// at seal time -- not after its write completes (writeAndFinish used to
	// do this, "after a successful import"). recordSealedOnParent is what the
	// guard above reads; recording it late left a window, widest with
	// N42_LEADER_WRITE_ASYNC=1 but present on any slow write, where a second,
	// independently-building seal on the SAME parent (a parked speculative
	// task finishing while this block's write is still in flight) found
	// firstSealedOnParent empty and proceeded to push/propose a divergent
	// sibling instead of being suppressed here. Moving the record here closes
	// that window; the vote-rule fix in the hotstuff package is the
	// independent, sufficient backstop if this one is ever missed.
	w.recordSealedOnParent(parentHash, blk)

	var (
		sealhash = w.engine.SealHash(blk.Header())
		hash     = blk.Hash()
	)
	w.mu.RLock()
	task, exist := w.pendingTasks[sealhash]
	var sealStart time.Time
	if exist {
		sealStart = task.sealStart
	}
	w.mu.RUnlock()
	if !exist {
		log.Error("Block found but no relative pending task", "number", blockNumber.Uint64(), "sealhash", sealhash, "hash", hash)
		return
	}
	if task.lagged {
		// P2a: built without execution; nothing to check against applied
		// state and nothing to write here.
		w.finishLaggedSeal(blk, sealhash, task)
		return
	}

	// Deep copy receipts and set block location fields to prevent write-write conflicts
	// when different blocks share the same sealhash.

	// Push-before-write (N42_PUSH_BEFORE_WRITE, off by default): hand
	// the sealed block to peers BEFORE committing it, so their import
	// runs alongside this node's write instead of after it. The
	// stale-seal predicate the write path would apply is evaluated
	// first, against a read snapshot, so a block the write is about to
	// reject still reaches nobody. The Proposal is NOT moved: it stays
	// after a successful write below.
	var tPush time.Time
	var dPush time.Duration
	pushedEarly := false
	// The write path's stale-seal gate, evaluated first against a read
	// snapshot and decisive: a stale candidate is dropped before its write,
	// not only before its push. The gate inside the write runs only for
	// isolated QMDB seals; a build whose speculative reload failed fell back
	// to the live computer, so its stale block reached CommitBlock, read
	// through the build's rolled-back transaction and panicked the worker
	// (round 35r, node3).
	var tCheckEnter time.Time
	var dCheck time.Duration
	c, hasCheck := w.chain.(sealParentChecker)
	if hasCheck {
		if contentionDiagEnabled {
			tCheckEnter = time.Now()
		}
		cerr := w.checkSealParentApplied(c, blk, parentHash)
		if contentionDiagEnabled {
			dCheck = time.Since(tCheckEnter)
		}
		if cerr != nil {
			log.Info("miner: sealed block is stale before its write; dropping",
				"number", blockNumber.Uint64(), "hash", hash.Hex()[:12], "err", cerr)
			w.releaseLostFrames(blk, nil)
			return
		}
	}
	if PushBeforeWrite() {
		// Fail safe: without the stale-seal check there is no way to know
		// the write would accept this block, so keep today's order rather
		// than broadcast one the write may reject.
		canPush := hasCheck
		if !hasCheck {
			log.Warn("push-before-write: chain cannot answer the stale-seal check; keeping write-then-push")
		}
		if canPush {
			tPush = time.Now()
			if perr := w.chain.SealedBlock(blk); perr != nil {
				log.Error("push-before-write: broadcast failed", "err", perr)
			} else {
				pushedEarly = true
			}
			dPush = time.Since(tPush)
		}
	}
	// Propose-before-write (N42_PROPOSE_BEFORE_WRITE, off by default,
	// needs the early push): hand the Proposal to the engine as soon
	// as the body is with the peers, so the prepare round and the
	// followers' imports run beside this node's write. Round 33: with
	// only the push moved, the follower's import started earlier but
	// the view still waited for the Proposal that trailed a ~150 ms
	// write at ~95k transactions. The engine's onBlockReady reads
	// nothing from the database; the leader's durable consensus state
	// is its vote journal, written before any signature leaves; and a
	// write that fails after this point leaves a block the leader
	// itself must re-fetch, which the followers decide on regardless.
	proposedEarly := false
	var tProposeEarly time.Time
	var dProposeEarly time.Duration
	if pushedEarly && ProposeBeforeWrite() {
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			if contentionDiagEnabled {
				tProposeEarly = time.Now()
			}
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
			if contentionDiagEnabled {
				dProposeEarly = time.Since(tProposeEarly)
			}
			proposedEarly = true
		}
	}

	// The receipts copy (163k allocations) is needed by the write, not by
	// the push or the Proposal: done after both leave.
	var tCopyStart time.Time
	if contentionDiagEnabled {
		tCopyStart = time.Now()
	}
	receipts := make([]*block.Receipt, len(task.receipts))
	var logs []*block.Log
	for i, taskReceipt := range task.receipts {
		receipt := new(block.Receipt)
		*receipt = *taskReceipt
		receipt.BlockHash = hash
		receipt.BlockNumber = blk.Number64()
		receipt.TransactionIndex = uint(i)

		receipt.Logs = make([]*block.Log, len(taskReceipt.Logs))
		for j, taskLog := range taskReceipt.Logs {
			lg := new(block.Log)
			*lg = *taskLog
			lg.BlockHash = hash
			receipt.Logs[j] = lg
		}
		receipts[i] = receipt
		logs = append(logs, receipt.Logs...)
	}
	var dCopy time.Duration
	if contentionDiagEnabled {
		dCopy = time.Since(tCopyStart)
	}

	if task.exec != nil {
		if eh, ok := w.chain.(interface {
			RememberExecutedResult(types.Hash, rawdb.ExecutedResult)
		}); ok {
			eh.RememberExecutedResult(blk.Hash(), *task.exec)
		}
	}

	// S23 (docs/QS_BLOCK_TIME_BUDGET.md 6ct/6cu, N42_LEADER_WRITE_ASYNC=1):
	// everything from here on -- the S19 journal-latch wait, the write
	// itself, and everything that today assumes "the write already
	// returned" (pendingTasks cleanup, counters, the seal-path/propose-
	// phases/"Successfully sealed" log lines, the ChainHighestBlock event) --
	// moves into writeAndFinish, called either
	// synchronously here (switch off: byte-for-byte today's control flow,
	// same goroutine, same order) or from the dedicated writer goroutine
	// (switch on: resultLoop returns immediately after Enqueue, free to
	// receive and push/propose the NEXT sealed result without waiting for
	// THIS write).
	job := &writeJob{
		blk: blk, receipts: receipts, logs: logs, task: task,
		sealhash: sealhash, hash: hash, parentHash: parentHash, blockNumber: blockNumber.Uint64(),
		sealStart: sealStart, tHandleSealedEnter: tHandleSealedEnter,
		tCheckEnter: tCheckEnter, dCheck: dCheck,
		tCopyStart: tCopyStart, dCopy: dCopy,
		tPush: tPush, dPush: dPush, pushedEarly: pushedEarly,
		tProposeEarly: tProposeEarly, dProposeEarly: dProposeEarly, proposedEarly: proposedEarly,
	}
	if w.asyncWriter != nil {
		w.asyncWriter.Enqueue(job)
		return
	}
	w.writeAndFinish(job)
}

// releaseLostFrames makes the frames of a sealed frame block that lost
// (dropped as a divergent or stale sibling) fillable again at once, except
// those the block kept in its place also holds (S90 defect 2: the loser's
// frames stayed in flight and every later frame of their senders skipped).
func (w *worker) releaseLostFrames(lost, kept block.IBlock) {
	if lost == nil {
		return
	}
	bc, ok := w.chain.(*internal.BlockChain)
	if !ok {
		return
	}
	body, ok := lost.Body().(*block.Body)
	if !ok || !body.IsFrameBody() {
		return
	}
	hashes := body.FrameHashes
	if kept != nil {
		if kb, ok := kept.Body().(*block.Body); ok && kb.IsFrameBody() {
			keep := make(map[types.Hash]struct{}, len(kb.FrameHashes))
			for _, h := range kb.FrameHashes {
				keep[h] = struct{}{}
			}
			out := make([]types.Hash, 0, len(hashes))
			for _, h := range hashes {
				if _, ok := keep[h]; !ok {
					out = append(out, h)
				}
			}
			hashes = out
		}
	}
	if n := bc.ReleaseFramesInFlight(hashes, lost.Number64().Uint64()); n > 0 {
		log.Info("miner: frames of a losing sealed block fillable again", "number", lost.Number64().Uint64(),
			"hash", lost.Hash().Hex()[:12], "frames", n)
	}
}

// writeAndFinish runs the S19 journal-latch wait, WriteBlockWithState
// itself, and everything handleSealed does today only after a successful
// write -- extracted so the switch-off (synchronous, called directly from
// handleSealed) and switch-on (called from asyncBlockWriter.run, S23,
// N42_LEADER_WRITE_ASYNC=1) paths share IDENTICAL logic. Returns whether
// the write succeeded (informational only; the caller does not currently
// branch on it -- a failed write already logs and returns exactly as
// handleSealed always has).
func (w *worker) writeAndFinish(job *writeJob) bool {
	blk, task := job.blk, job.task
	hash, sealhash := job.hash, job.sealhash
	blockNumber := job.blockNumber
	receipts, logs := job.receipts, job.logs
	sealStart, tHandleSealedEnter := job.sealStart, job.tHandleSealedEnter
	tCheckEnter, dCheck := job.tCheckEnter, job.dCheck
	tCopyStart, dCopy := job.tCopyStart, job.dCopy
	tPush, dPush, pushedEarly := job.tPush, job.dPush, job.pushedEarly
	tProposeEarly, dProposeEarly, proposedEarly := job.tProposeEarly, job.dProposeEarly, job.proposedEarly

	// N42_LEADER_WRITE_AFTER_JOURNAL (S19, docs/QS_BLOCK_TIME_BUDGET.md 6cn/
	// 6co): delay the START of the write below until this node's own
	// journalCommitVote for THIS block has succeeded (or the configured
	// timeout elapses, or the view is abandoned), so the journal's small
	// MDBX write finds the writer idle instead of queueing behind this one.
	// Only meaningful when the Proposal already left (proposedEarly): that is
	// what starts the chain of events (onBlockReady -> tryFormPrepareQC ->
	// journalCommitVote) this wait is waiting on. Without an early Proposal
	// (switch off, or push-before-write off/failed) NotifyBlockSealed has not
	// even run yet at this point -- there is nothing to wait for, so this is
	// skipped rather than spending the full timeout on a signal that cannot
	// possibly arrive yet. Never holds w.mu or any chain lock while waiting.
	// S23: this wait now runs on the dedicated writer goroutine when the
	// switch is on, not on resultLoop -- moving it there too (not just the
	// write) is what keeps resultLoop free the whole time, matching the
	// task's own requirement that the S19 latch wait "must NOT block
	// resultLoop either".
	var lwWait time.Duration
	lwWhy := "off"
	if LeaderWriteAfterJournalOn() {
		if !proposedEarly {
			lwWhy = "not-proposed-yet"
		} else if cw, ok := w.engine.(commitVoteJournalWaiter); ok {
			lwWait, lwWhy = cw.WaitForCommitVoteJournal(hash, LeaderWriteAfterJournalTimeout())
		} else {
			lwWhy = "unsupported"
		}
	}

	tWrite := time.Now()
	err := w.chain.WriteBlockWithState(blk, receipts, task.state, task.nopay)
	dWrite := time.Since(tWrite)
	if err != nil {
		if proposedEarly {
			log.Warn("propose-before-write: the write failed AFTER the Proposal left; this node re-fetches its own block if the fleet commits it",
				"number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "err", err)
		}
		if errors.Is(err, internal.ErrStaleSeal) {
			// The applied head moved past this seal's parent while it
			// was in flight (a competing same-height candidate won, OR --
			// S23 only -- an earlier queued write of this node's OWN onto
			// the same parent chain failed; see pendingWrite's doc comment,
			// async_write.go, for why this path is exactly as safe here as
			// it always has been for an ordinary sibling race).
			log.Info("Sealed block lost to a competing candidate; dropping",
				"number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
			return false
		}
		log.Error("Failed writing block to chain", "err", err)
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			bc.ForgetSealedHeader(blk.Hash())
		}
		miningErrorsCounter.Inc()
		return false
	}
	w.mu.Lock()
	delete(w.pendingTasks, sealhash)
	w.mu.Unlock()
	// The sealed block is stored: build its mobile-verification packet from the
	// sealed header so it is keyed by, and carries, the hash a verifier knows.
	task.mobilePacket.launch(blk, w.mobilePacketSink)
	blocksMinedCounter.Inc()
	blockMiningTimer.UpdateDuration(task.createdAt)

	// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct): ONE line covering every
	// named step from this view's build trigger to the push, plus the two
	// queue waits U1 asked for directly. sealStart/blsNanos double as
	// "engine.Seal enter"/"BLS sign start-end" (Seal's own call is bracketed
	// by exactly these two existing timers, taskLoop's tSeal a few lines
	// above sealStart); resQWaitMs is the gap between Seal() itself finishing
	// (sealStart+blsNanos) and resultLoop actually receiving this result
	// (tHandleSealedEnter) -- queued behind whatever handleSealed(v) was
	// still doing (its own write, most likely) on this single goroutine.
	// taskQWaitMs is the same idea for taskCh (taskChSentAt -> sealStart).
	// S23 adds wqWaitMs/wqDepth: the NEW queue wait, this write's own
	// enqueue call on resultLoop (zero when the switch is off, or when this
	// write's own enqueue did not have to block).
	if contentionDiagEnabled {
		blsEnd := sealStart.Add(time.Duration(task.blsNanos.Load()))
		log.Info("miner: seal path",
			"number", blockNumber,
			"triggerTMs", tMs(task.triggerAt),
			"buildBeginTMs", tMs(task.buildBeginAt),
			"specParkedTMs", tMs(task.specParkedAt),
			"specHitTMs", tMs(task.specHitAt),
			"paceEnterTMs", tMs(task.paceEnterAt),
			"paceDurMs", task.paceDur.Milliseconds(),
			"taskSentTMs", tMs(task.taskChSentAt),
			"taskQWaitMs", waitMs(task.taskChSentAt, sealStart),
			"taskPickedTMs", tMs(sealStart),
			"sealEnterTMs", tMs(sealStart),
			"checkEnterTMs", tMs(tCheckEnter),
			"checkExitTMs", tMs(tCheckEnter.Add(dCheck)),
			"blsStartTMs", tMs(sealStart),
			"blsEndTMs", tMs(blsEnd),
			"resultRecvTMs", tMs(tHandleSealedEnter),
			"resQWaitMs", waitMs(blsEnd, tHandleSealedEnter),
			"copyStartTMs", tMs(tCopyStart),
			"copyEndTMs", tMs(tCopyStart.Add(dCopy)),
			"pushStartTMs", tMs(tPush),
			"pushEndTMs", tMs(tPush.Add(dPush)),
			"proposeStartTMs", tMs(tProposeEarly),
			"proposeEndTMs", tMs(tProposeEarly.Add(dProposeEarly)),
			"lwWaitMs", lwWait.Milliseconds(),
			"lwWhy", lwWhy,
			"writeStartTMs", tMs(tWrite),
			"writeEndTMs", tMs(tWrite.Add(dWrite)),
			"wqWaitMs", job.wqWaitMs,
			"wqDepth", job.wqDepth,
		)
	}

	body := blk.Body()
	var verifierCount, rewardCount int
	if body != nil {
		verifierCount = len(body.Verifier())
		rewardCount = len(body.Reward())
	}
	blockSignGauge.Set(uint64(verifierCount))

	if len(logs) > 0 {
		event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
	}

	log.Info("🔨 Successfully sealed new block",
		"sealhash", sealhash,
		"hash", hash,
		"number", blockNumber,
		"used gas", blk.GasUsed(),
		"diff", blk.Difficulty().Uint64(),
		"headerTime", time.Unix(int64(blk.Time()), 0).Format(time.RFC3339),
		"verifierCount", verifierCount,
		"rewardCount", rewardCount,
		"elapsed", common.PrettyDuration(time.Since(task.createdAt)),
		"txs", len(blk.Transactions()))

	if !pushedEarly {
		tPush = time.Now()
		if err = w.chain.SealedBlock(blk); err != nil {
			log.Error("Failed Broadcast block to p2p network", "err", err)
			return true
		}
		dPush = time.Since(tPush)
	}
	// For leader-driven consensus (HotStuff), start the Proposal for THIS
	// exact sealed block — the one we just persisted and direct-pushed — so
	// the proposed (and committed) block is byte-for-byte what followers
	// receive and import. Doing this here (not in Seal) binds propose↔push to
	// the same block, which import-gated voting requires.
	tNotify := time.Now()
	if bpn, ok := w.engine.(blockPersistenceNotifier); ok {
		bpn.NotifyBlockPersisted(blk.Hash(), blk.TxHash(), blk.ParentHash())
	}
	if !proposedEarly {
		if bsn, ok := w.engine.(blockSealNotifier); ok {
			bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
		}
	}
	dNotify := time.Since(tNotify)

	// Leader seal→propose breakdown: ONE line per produced block covering
	// the window "miner: build phases" stops measuring at (it is emitted
	// before commit()) up to the Proposal being handed to the engine.
	// `write` is the whole WriteBlockWithState — "blockwrite phases"
	// (blockchain_write.go) splits it further. `lwWait`/`lwWhy` (S19,
	// N42_LEADER_WRITE_AFTER_JOURNAL) is the wait spliced in just before
	// `write` starts, if the switch is on: how long this write's own start
	// was delayed, and why it stopped waiting ("journal": the commit-vote
	// journal succeeded; "abandoned": the view timed out before that;
	// "timeout": neither happened inside the configured ceiling; "off": the
	// switch is unset; "not-proposed-yet": no early Proposal to wait on;
	// "unsupported": w.engine does not implement commitVoteJournalWaiter).
	log.Info("miner: propose phases",
		"n", blockNumber, "txs", len(blk.Transactions()),
		"finalize", task.finalize, "witness", task.witness, "assemble", task.assemble,
		"bls", time.Duration(task.blsNanos.Load()), "seal2res", time.Since(sealStart),
		"write", dWrite, "push", dPush, "pushedEarly", pushedEarly, "proposedEarly", proposedEarly, "notify", dNotify,
		"lwWait", lwWait, "lwWhy", lwWhy,
		"total", time.Since(task.createdAt), "tMs", time.Now().UnixMilli())

	// S26: recordSealedOnParent now runs in handleSealed, at seal time,
	// before push/propose -- see the comment there. Recording it again here
	// would be a no-op (first write wins) kept only as history.
	if concrete, ok := blk.(*block.Block); ok {
		event.GlobalEvent.Send(common.ChainHighestBlock{Block: *concrete, Inserted: true})
	}
	return true
}

// firstSealedOnParent returns the first block this node sealed+imported on the
// given parent, or nil. Used to suppress a later view's divergent same-height
// sibling (see the sealedOnParent field doc).
// rememberSealed records a just-sealed block by hash (bounded to the last
// 16 by number) so a speculative build can extend it before it is written.
func (w *worker) rememberSealed(blk block.IBlock) {
	if blk == nil {
		return
	}
	if bc, ok := w.chain.(*internal.BlockChain); ok {
		if h, ok := blk.Header().(*block.Header); ok {
			bc.RememberSealedHeader(h)
		}
	}
	sealhash := w.engine.SealHash(blk.Header())
	// Only the header is ever read back (ownSealed callers), and the full
	// block is the very object the import resolves: kept whole, every
	// transaction it decodes stays reachable from here.
	slim := slimSealedBlock(blk)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sealedByHash[blk.Hash()] = slim
	if t := w.pendingTasks[sealhash]; t != nil {
		if t.post != nil {
			w.sealedPost[blk.Hash()] = t.post
		}
		if t.exec != nil {
			w.sealedExec[blk.Hash()] = *t.exec
		}
	}
	if len(w.sealedByHash) > 16 {
		var oldest types.Hash
		oldestNum := ^uint64(0)
		for h, b := range w.sealedByHash {
			if n := b.Number64(); n != nil && n.Uint64() < oldestNum {
				oldestNum, oldest = n.Uint64(), h
			}
		}
		delete(w.sealedByHash, oldest)
		delete(w.sealedPost, oldest)
		delete(w.sealedExec, oldest)
	}
}

// unwrittenOwnPostStates returns the post-state snapshots of the own sealed
// blocks from parent down to (excluding) the applied lineage, newest first.
// nil when any block on that path is not ours or has no snapshot: the build
// then waits for the parent's write like a plain speculative build.
func (w *worker) unwrittenOwnPostStates(parent types.Hash, bc *internal.BlockChain) []*state.PostState {
	var out []*state.PostState
	h := parent
	for range 16 {
		own := w.ownSealed(h)
		if own == nil || own.Number64() == nil {
			return nil
		}
		w.mu.RLock()
		post := w.sealedPost[h]
		w.mu.RUnlock()
		if post == nil {
			return nil
		}
		out = append(out, post)
		num := own.Number64().Uint64()
		if num == 0 || bc.HasAppliedBlock(own.ParentHash(), num-1) {
			return out
		}
		h = own.ParentHash()
	}
	return nil
}

func (w *worker) ownSealed(hash types.Hash) block.IBlock {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sealedByHash[hash]
}

// checkSealParentApplied is handleSealed's stale-seal gate, factored out so
// the S23 bypass decision is independently testable. c.CheckSealParentApplied
// is the real, authoritative-as-of-last-commit DB check; the bypass (when
// N42_LEADER_WRITE_ASYNC=1) additionally treats blk's parent as applied when
// it matches whatever w.asyncWriter itself most recently accepted for
// writing -- see pendingWrite's doc comment (async_write.go) for why an
// optimistic pass here is safe even when it turns out to be wrong.
func (w *worker) checkSealParentApplied(c sealParentChecker, blk block.IBlock, parentHash types.Hash) error {
	if w.asyncWriter != nil {
		if expHash, _, ok := w.asyncWriter.ExpectedParent(); ok && expHash == parentHash {
			return nil
		}
	}
	return c.CheckSealParentApplied(blk)
}

// ownPendingSpeculation reports whether this speculative build extends a
// block this node sealed and has not applied yet. The miner tree already
// holds that block's post-state (its own build), so the build neither waits
// for the write nor aligns the applied branch -- the write of the parent
// and the hit check (AppliedHeadIs at hand-over) keep it honest, and a lost
// parent takes the child down with it (PeelAll on the next build).
func (w *worker) ownPendingSpeculation(speculative bool, parent types.Hash, bc *internal.BlockChain) bool {
	if !speculative || bc == nil {
		return false
	}
	own := w.ownSealed(parent)
	if own == nil || own.Number64() == nil {
		return false
	}
	return !bc.AppliedHeadIsExactly(parent, own.Number64().Uint64())
}

func (w *worker) firstSealedOnParent(parent types.Hash) block.IBlock {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sealedOnParent[parent]
}

// recordSealedOnParent records blk as the sole candidate for its parent (first
// SEAL wins -- called from handleSealed before push/propose, S26; a write that
// is still in flight when a second, independently-built seal on the same
// parent arrives must not leave this map empty) and prunes entries older than
// the 256-block branch-switch window — a sibling that far back can no longer
// be unwound/switched, so it needs no suppression record.
//
// A kept block is re-proposed whole only while its height is undecided; at or
// below the written head the guard reads its hash and frame list alone. Those
// entries are slimmed to that (slimSealedBlock) on every record: the kept
// block is the object the import resolves, so holding it whole held every
// decoded transaction of every block this node sealed in the window -- ~82
// blocks of 75k at +150 s on a 7-node fleet (3.5 GB, round S97), heading for
// 512.
func (w *worker) recordSealedOnParent(parent types.Hash, blk block.IBlock) {
	var head uint64
	haveHead := false
	if w.chain != nil {
		if cur := w.chain.CurrentBlock(); cur != nil && cur.Number64() != nil {
			head, haveHead = cur.Number64().Uint64(), true
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.sealedOnParent[parent]; !ok {
		w.sealedOnParent[parent] = blk
	}
	if haveHead {
		if w.sealedOnParentSlim == nil {
			w.sealedOnParentSlim = make(map[types.Hash]struct{})
		}
		for p, b := range w.sealedOnParent {
			if _, done := w.sealedOnParentSlim[p]; done || b.Number64().Uint64() > head {
				continue
			}
			w.sealedOnParent[p] = slimSealedBlock(b)
			w.sealedOnParentSlim[p] = struct{}{}
		}
	}
	if len(w.sealedOnParent) <= 512 {
		return
	}
	number := blk.Number64().Uint64()
	if number <= 256 {
		return
	}
	cutoff := number - 256
	for p, b := range w.sealedOnParent {
		if b.Number64().Uint64() < cutoff {
			delete(w.sealedOnParent, p)
			delete(w.sealedOnParentSlim, p)
		}
	}
}

// slimSealedBlock returns a new block without blk's transactions and frame
// bytes: the same header (so the same hash) and, for a frame block, the same
// frame hash list. blk is returned unchanged when the copy would not hash the
// same.
func slimSealedBlock(blk block.IBlock) block.IBlock {
	b, ok := blk.(*block.Block)
	if !ok || b == nil {
		return blk
	}
	// A copy is made even while the body holds nothing yet: a lag-built block
	// is remembered before the import resolves it in place.
	body, ok := b.Body().(*block.Body)
	if !ok || body == nil {
		return blk
	}
	hdr, ok := b.Header().(*block.Header)
	if !ok || hdr == nil {
		return blk
	}
	// NewBlock copies the header untouched; the frame list is shared, never
	// mutated after assembly.
	s := block.NewBlock(hdr, nil)
	if body.IsFrameBody() {
		sb, ok := s.Body().(*block.Body)
		if !ok {
			return blk
		}
		sb.FrameHashes = body.FrameHashes
	}
	if s.Hash() != b.Hash() {
		return blk
	}
	return s
}
