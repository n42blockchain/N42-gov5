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
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// broadcastTxs forwards locally accepted transactions to the gossip mesh.
// Remote-received transactions also fire NewTxsEvent after AddRemotes; gossip
// dedup (message-id cache) absorbs the re-publish, and the pool rejects
// duplicates, so no loop forms.
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
		var published, failed uint64
		for {
			select {
			case <-s.ctx.Done():
				return
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
					if err := s.cfg.p2p.BroadcastTransaction(s.ctx, rlpBytes); err != nil {
						failed++
						// Loud: a silent Debug here cost a diagnosis round —
						// every prior tx-path failure in this codebase hid the
						// same way.
						if failed <= 3 || failed%100 == 0 {
							log.Warn("tx broadcaster: publish failed", "hash", tx.Hash(), "failed", failed, "err", err)
						}
						continue
					}
					published++
					if published == 1 || published%500 == 0 {
						log.Info("tx broadcaster: publishing", "published", published, "failed", failed)
					}
				}
			}
		}
	}()
}
