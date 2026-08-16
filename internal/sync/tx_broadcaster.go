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

package sync

import (
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// txBatchFlushInterval is how long a partial batch may wait for company before
// it is published anyway. Small against the 2s block interval (propagation
// latency it adds is invisible), large against the per-message overhead it
// amortises: at ~11k tx/s a 25ms window fills a ~275-transaction batch.
const txBatchFlushInterval = 25 * time.Millisecond

// broadcastTxs forwards LOCALLY SUBMITTED transactions to the gossip mesh, in
// batches.
//
// NewLocalTxsEvent, not NewTxsEvent, on purpose: gossip-received transactions
// need no re-publish — the mesh already forwarded the message to every peer,
// and gossipsub's IHAVE/IWANT heartbeat repairs any gap. Re-publishing them
// was free only while one message carried one transaction (the re-publish was
// byte-identical, so the content-addressed message-id deduplicated it); a
// re-batched copy has a new payload and would genuinely go out again from
// every node — a 7x amplification on a 7-node mesh.
//
// One message per transaction was ~24% of saturated-fleet CPU in network
// writes (see tx_batch_wire.go); transactions therefore coalesce for up to
// txBatchFlushInterval or until the batch hits its size caps, whichever comes
// first.
func (s *Service) broadcastTxs() {
	if !s.cfg.txGossipEnabled {
		return // no pool wired (or gossip disabled) — nothing to publish
	}
	txsCh := make(chan common.NewLocalTxsEvent, 256)
	sub, err := event.GlobalEvent.Subscribe(txsCh)
	if err != nil {
		log.Error("tx broadcaster: subscribe failed", "err", err)
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer sub.Unsubscribe()
		var published, failed uint64

		var (
			batch      [][]byte
			batchBytes int
		)
		// Created stopped: it is armed only while a partial batch waits.
		timer := time.NewTimer(txBatchFlushInterval)
		if !timer.Stop() {
			<-timer.C
		}
		defer timer.Stop()
		timerArmed := false

		flush := func() {
			if len(batch) == 0 {
				return
			}
			payload, err := encodeTxBatch(batch)
			n := uint64(len(batch))
			batch, batchBytes = batch[:0], 0
			if err != nil {
				failed += n
				log.Warn("tx broadcaster: batch encode failed", "txs", n, "failed", failed, "err", err)
				return
			}
			if err := s.cfg.p2p.BroadcastTransaction(s.ctx, payload); err != nil {
				failed += n
				// Loud: a silent Debug here cost a diagnosis round — every
				// prior tx-path failure in this codebase hid the same way.
				if failed <= 3 || failed%100 == 0 {
					log.Warn("tx broadcaster: publish failed", "txs", n, "failed", failed, "err", err)
				}
				return
			}
			before := published
			published += n
			if before == 0 || before/500 != published/500 {
				log.Info("tx broadcaster: publishing", "published", published, "failed", failed, "batch", n)
			}
		}

		for {
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
				timerArmed = false
				flush()
			case ev := <-txsCh:
				for _, tx := range ev.Txs {
					if tx == nil {
						continue
					}
					// Standard Ethereum encoding: 41% fewer bytes on the wire
					// than the SSZ-over-protobuf form this used to publish,
					// and no throwaway proto struct per transaction per hop.
					rlpBytes, err := transaction.EncodeEthereumTransaction(tx)
					if err != nil {
						failed++
						if failed <= 3 || failed%100 == 0 {
							log.Warn("tx broadcaster: encode failed", "hash", tx.Hash(), "failed", failed, "err", err)
						}
						continue
					}
					batch = append(batch, rlpBytes)
					batchBytes += len(rlpBytes)
					if len(batch) >= txBatchMaxTxs || batchBytes >= txBatchMaxBytes {
						if timerArmed && !timer.Stop() {
							<-timer.C
						}
						timerArmed = false
						flush()
					}
				}
				if len(batch) > 0 && !timerArmed {
					timer.Reset(txBatchFlushInterval)
					timerArmed = true
				}
			}
		}
	}()
}
