// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// GossipSub PUBLISHER for mempool transactions — the missing half of
// subscriber_transactions.go. The txpool emits NewTxsEvent for every locally
// accepted transaction, but nothing forwarded those to the transaction gossip
// topic, so local transactions never left the node: on a leader-rotating
// (HotStuff) chain only the submitting node's own leader turns could include
// them — 1/N throughput — while every other validator sealed empty blocks and
// the submitter's pool backlog grew until its builds overran the view window
// (observed live: one tx-bearing block per ~7, hundreds of pending stuck).
//
// What travels is the hash, not the body. See p2p.GossipTxHashesMessage.

package sync

import (
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

const (
	// maxAnnounceBatch bounds one announcement. 1024 hashes is 32 KiB, which
	// stays well inside the gossip cap at its 1 MiB default.
	maxAnnounceBatch = 1024

	// announceInterval is how long a partial batch waits for company.
	//
	// Announcing per transaction is what the body broadcast did, and at
	// saturation the resulting wakeup rate — one publish, one goroutine, one
	// write syscall per transaction — showed up as ~40% of the node's CPU
	// sitting in the scheduler (findRunnable/stealWork spinning, lock2 parking)
	// while only 4 of 32 cores were busy. Batching trades a few tens of
	// milliseconds of propagation delay for an order of magnitude fewer
	// wakeups; at 11k transactions/s a 50 ms window fills a full batch anyway,
	// so under load the delay is bounded by batch size, not by this timer.
	announceInterval = 50 * time.Millisecond
)

// broadcastTxs announces accepted transactions to the gossip mesh by hash.
// Remote-received transactions also fire NewTxsEvent after AddRemotes, which
// is what carries an announcement onward through the mesh; gossip dedup (the
// message-id cache) absorbs re-announcements and the pool rejects duplicates,
// so no loop forms.
func (s *Service) broadcastTxs() {
	if !s.cfg.txGossipEnabled {
		return // no pool wired (or gossip disabled) — nothing to publish
	}
	txsCh := make(chan common.NewTxsEvent, 256)
	sub, err := event.GlobalEvent.Subscribe(txsCh)
	if err != nil {
		log.Error("tx broadcaster: subscribe failed", "err", err)
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer sub.Unsubscribe()

		var (
			batch     = make([]types.Hash, 0, maxAnnounceBatch)
			announced uint64
			failed    uint64
			timer     = time.NewTimer(announceInterval)
		)
		defer timer.Stop()
		if !timer.Stop() {
			<-timer.C
		}
		timerArmed := false

		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := s.cfg.p2p.BroadcastTxHashes(s.ctx, batch); err != nil {
				failed += uint64(len(batch))
				// Loud: a silent Debug here cost a diagnosis round — every
				// prior tx-path failure in this codebase hid the same way.
				if failed <= 3 || failed%1000 == 0 {
					log.Warn("tx broadcaster: announce failed", "batch", len(batch), "failed", failed, "err", err)
				}
			} else {
				announced += uint64(len(batch))
				if announced <= uint64(len(batch)) || announced%50000 < uint64(len(batch)) {
					log.Info("tx broadcaster: announcing", "announced", announced, "failed", failed, "batch", len(batch))
				}
			}
			batch = batch[:0]
		}

		for {
			select {
			case <-s.ctx.Done():
				return
			case ev := <-txsCh:
				for _, tx := range ev.Txs {
					if tx == nil {
						continue
					}
					batch = append(batch, tx.Hash())
					if len(batch) >= maxAnnounceBatch {
						flush()
						if timerArmed && !timer.Stop() {
							<-timer.C
						}
						timerArmed = false
					}
				}
				if len(batch) > 0 && !timerArmed {
					timer.Reset(announceInterval)
					timerArmed = true
				}
			case <-timer.C:
				timerArmed = false
				flush()
			}
		}
	}()
}
