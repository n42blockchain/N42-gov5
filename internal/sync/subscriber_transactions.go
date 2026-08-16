// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// GossipSub subscriber for mempool transactions. txSubscriber RLP-decodes
// the standard Ethereum transaction encoding into the internal
// transaction.Transaction type and adds it to the txPool via AddRemotes,
// swallowing decode errors so misbehaving peers are not immediately
// penalised.

package sync

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
)

// txGossipReceived counts transactions accepted from gossip (diagnostic; the
// first and every 500th are logged at Info so a dead pipeline is visible).
var txGossipReceived atomic.Uint64

// txSubscriber handles incoming transaction messages from GossipSub.
//
// A message is either a batch (txBatchMarker framing, see tx_batch_wire.go) or
// one transaction in the standard Ethereum encoding (legacy RLP or an EIP-2718
// typed envelope) — the single form remains accepted for peers publishing
// mid-rolling-upgrade. Neither encoding carries a sender field, so the sender
// is whatever the signature recovers to and a peer cannot assert one; the pool
// recovers it during validation, and a batch lets that recovery fan out across
// the pool's pre-warm workers instead of running once per message.
func (s *Service) txSubscriber(ctx context.Context, data any) error {
	raw, ok := data.(*rawSSZBytes)
	if !ok {
		return nil
	}

	txs, err := decodeTxBatch(raw.data)
	if err != nil {
		if !errors.Is(err, errNotABatch) {
			log.Warn("Failed to decode gossiped transaction batch", "err", err)
			return nil // don't return error to avoid penalizing peer
		}
		tx, err := transaction.DecodeEthereumTransaction(raw.data)
		if err != nil {
			log.Warn("Failed to decode gossiped transaction", "err", err)
			return nil
		}
		txs = []*transaction.Transaction{tx}
	}

	errs := s.cfg.txPool.AddRemotes(txs)
	accepted := uint64(0)
	for i, err := range errs {
		if err == nil {
			accepted++
		} else if i < len(txs) {
			log.Debug("Gossiped transaction rejected by pool", "hash", txs[i].Hash(), "err", err)
		}
	}
	if accepted > 0 {
		before := txGossipReceived.Add(accepted) - accepted
		if before == 0 || before/500 != (before+accepted)/500 {
			log.Info("tx gossip: receiving", "received", before+accepted)
		}
	}
	return nil
}
