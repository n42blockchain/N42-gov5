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
// Worker event loops: run, task, work loops and block pacing.

package miner

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// shouldProduceNow reports whether this node should build a block now. Timer/PoW
// engines always produce. Leader-driven engines (HotStuff) produce only when this
// node is the current view's leader, so a single node produces each block (then
// direct-pushes it to peers); otherwise every node forks its own chain.
func (w *worker) shouldProduceNow() bool {
	if usesTimerDrivenSealing(w.engine) {
		return true
	}
	if la, ok := w.engine.(leaderAware); ok {
		return la.IsCurrentLeader()
	}
	return true
}

func (w *worker) runLoop() error {
	defer w.cancel()
	defer w.stop()
	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case req := <-w.newWorkCh:
			if !req.enqueuedAt.IsZero() {
				log.Info("miner: work queue wait", "waitNs", time.Since(req.enqueuedAt).Nanoseconds(),
					"speculative", req.speculative)
			}
			err := w.commitWorkGuarded(req)
			if err != nil && req.speculative {
				// An aborted or failed guess costs nothing; do not alarm.
				log.Debug("speculative build abandoned", "err", err)
				err = nil
			}
			if err != nil {
				log.Error("runLoop error", "err", err)
			}
		}
	}
}

// commitWorkGuarded turns a panic inside one build into that build's error.
// runLoop stops the worker when it returns, so a panic that escaped from a
// build used to take the miner down for good (see handleSealed).
func (w *worker) commitWorkGuarded(req *newWorkReq) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("panic in miner build; abandoning it: %v\nstack: %s", r, debug.Stack())
			err = fmt.Errorf("panic in miner build: %v", r)
		}
	}()
	return w.commitWork(req.interrupt, req.noempty, req.timestamp, req.parentHash, req.speculative, req.enqueuedAt)
}

func (w *worker) taskLoop() error {
	defer w.cancel()
	defer w.stop()

	var (
		stopCh chan struct{}
		prev   types.Hash
	)

	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case task := <-w.taskCh:

			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}

			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev && usesTimerDrivenSealing(w.engine) {
				// Timer/PoW engines: the recommit loop re-submits identical work —
				// skip. Leader-driven engines (HotStuff) must NOT dedup here: block
				// time is deterministic (parent time + period), so a NEW view's
				// leader legitimately rebuilds the IDENTICAL block and must re-enter
				// Seal to propose it in that view. Swallowing the retry wedges the
				// chain: the first build (possibly as a follower) poisons `prev`,
				// and every later leader's rebuild is silently dropped — no proposal
				// ever goes out while views time out forever (observed live on a
				// 7-node restart at a stalled height).
				continue
			}
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash
			w.mu.Lock()
			// Same critical section that publishes the task, so resultLoop's
			// pendingTasks read under RLock also observes sealStart.
			task.sealStart = time.Now()
			w.pendingTasks[sealHash] = task
			w.mu.Unlock()

			hash := task.block.Hash()
			stateRoot := task.block.StateRoot()
			tSeal := time.Now()
			// NOTE: do NOT push task.block here — it is unsealed. HotStuff seals the
			// block (appends the BLS sig to Extra), changing its hash, and proposes/
			// commits the SEALED block. The reliable direct push happens in
			// resultLoop after sealing, so followers receive the same sealed block
			// that consensus commits (otherwise CommitToCanonical can't find it).
			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				w.mu.Lock()
				delete(w.pendingTasks, sealHash)
				w.mu.Unlock()
				log.Warn("delete task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot, "err", err)
				if errors.Is(err, consensus.ErrNotEnoughSign) {
					time.Sleep(1 * time.Second)
					w.startCh <- struct{}{}
				}
			} else {
				task.blsNanos.Store(int64(time.Since(tSeal)))
				log.Debug("send task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot)
			}
		}
	}
}

// paceBlock throttles the wall-clock seal rate to a fixed interval on an
// absolute grid (drift-free over long runs). Set the anchor on the first paced
// block, then hold each subsequent block until its grid slot
// (anchor + (num-anchorNum)*interval). A block that runs late has a slot
// already in the past, so it seals immediately and the schedule self-corrects
// with no accumulated drift. Interval 0 disables the throttle entirely
// (produce flat out — e.g. benchmarking). Consensus is unaffected:
// header.Time stays the deterministic parent.Time+period value.
func (w *worker) paceBlock(num uint64) error {
	iv := w.minerConf.BlockIntervalMs
	if iv <= 0 || !w.isRunning() {
		return nil
	}
	interval := time.Duration(iv) * time.Millisecond
	if !w.pacingAnchorSet || num < w.pacingAnchorNum {
		w.pacingAnchorWall, w.pacingAnchorNum, w.pacingAnchorSet = time.Now(), num, true
		return nil
	}
	target := w.pacingAnchorWall.Add(time.Duration(num-w.pacingAnchorNum) * interval)
	wait := time.Until(target)
	// Cap a single wait at one interval. With rotating leaders each node
	// seals only every Nth block, so its own grid can demand up to
	// N*interval in one go — long enough to trip the view timeout and
	// start a TC storm. One-interval waits still pace the NETWORK to
	// ~interval per block (every leader holds its slot), and the solo
	// case (num advances by 1 per seal) is unaffected.
	if wait > interval {
		wait = interval
		w.pacingAnchorWall, w.pacingAnchorNum = time.Now().Add(interval), num
	}
	// Re-anchor if we have fallen more than one full interval behind the
	// grid (clock jump, long stall) so catch-up bursts stay bounded.
	if wait < -interval {
		w.pacingAnchorWall, w.pacingAnchorNum = time.Now(), num
	} else if wait > 0 {
		// Report the throttle's actual cost. paceBlock sits between commitWork
		// and sealStart -- on the speculative-hit path (91% of builds) it runs
		// immediately before the task reaches taskCh -- which is inside the
		// 247-373 ms window that ViewStart -> seal start has never accounted
		// for. Round 16 measured the two candidates it was aimed at, the
		// consensus gates and the miner's work queue, at 0.03 ms and 0.01 ms
		// combined, so the time is somewhere in here. Whether this throttle
		// fires under load has been ARGUED twice from the cadence and never
		// measured; the argument says it cannot (a late block's slot is in the
		// past, so wait is negative), and the decay phase's exact 250 ms/block
		// says it certainly does when blocks are cheap.
		log.Info("miner: pacing wait", "num", num, "waitNs", wait.Nanoseconds())
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-w.ctx.Done():
			timer.Stop()
			return w.ctx.Err()
		}
	}
	return nil
}

// takeSpecTask returns the parked speculative task when it extends parent, and
// clears the parking spot either way — a mismatch means the guess is stale.
func (w *worker) takeSpecTask(parent types.Hash) *task {
	w.specMu.Lock()
	defer w.specMu.Unlock()
	t := w.specTask
	match := t != nil && w.specParent == parent
	w.specTask, w.specParent = nil, types.Hash{}
	if match {
		return t
	}
	return nil
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	var (
		prevF = float64(prev.Nanoseconds())
		next  float64
	)
	if inc {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
		max := float64(maxRecommitInterval.Nanoseconds())
		if next > max {
			next = max
		}
	} else {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
		min := float64(minRecommit.Nanoseconds())
		if next < min {
			next = min
		}
	}
	return time.Duration(int64(next))
}

func (w *worker) workLoop(recommit time.Duration) error {
	defer w.cancel()
	defer w.stop()
	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	newBlockCh := make(chan common.ChainHighestBlock, 10)
	defer close(newBlockCh)

	newBlockSub, _ := event.GlobalEvent.Subscribe(newBlockCh)
	defer newBlockSub.Unsubscribe()

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	commit := func(noempty bool, s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}
		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, noempty: noempty, timestamp: timestamp}:
		case <-w.ctx.Done():
			return
		}
		timer.Reset(recommit)
	}

	clearPending := func(number *uint256.Int) {
		w.mu.Lock()
		for h, t := range w.pendingTasks {
			threshold := uint256.NewInt(0).Add(t.block.Number64(), uint256.NewInt(staleThreshold))
			if number.Cmp(threshold) >= 0 {
				delete(w.pendingTasks, h)
			}
		}
		w.mu.Unlock()
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()

		case <-w.startCh:
			clearPending(w.chain.CurrentBlock().Number64())
			// Leader-driven engines (HotStuff) produce ONLY via
			// TriggerBlockProduction, which pins the build to the consensus-
			// mandated parent (the HighQC/LockedQC block). An event-driven
			// commit() here builds on the local head with no pin — for a current
			// leader that is a second, competing build at the same height (a
			// sibling generator racing the pinned proposal).
			if !usesTimerDrivenSealing(w.engine) {
				continue
			}
			if !w.shouldProduceNow() {
				continue
			}
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case blockEvent := <-newBlockCh:
			clearPending(blockEvent.Block.Number64())
			// See the startCh case: no event-driven unpinned builds on
			// leader-driven engines.
			if !usesTimerDrivenSealing(w.engine) {
				continue
			}
			if !w.shouldProduceNow() {
				continue
			}
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case err := <-newBlockSub.Err():
			return err

		case <-timer.C:
			// HotStuff BFT: block production is leader-driven, not timer-driven.
			// Only the leader triggers production via TriggerBlockProduction().
			if w.isRunning() && usesTimerDrivenSealing(w.engine) {
				commit(true, commitInterruptResubmit)
			}

		case adjust := <-w.resubmitAdjustCh:
			before := recommit
			if adjust.inc {
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}
		}
	}
}
