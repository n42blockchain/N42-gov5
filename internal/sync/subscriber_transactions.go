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

	"github.com/libp2p/go-libp2p/core/peer"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
)

// txGossipReceived counts transactions accepted from gossip (diagnostic; the
// first and every 500th are logged at Info so a dead pipeline is visible).
var txGossipReceived atomic.Uint64

// txSubscriber handles incoming transaction messages from GossipSub.
//
// Each message is one transaction in the standard Ethereum encoding (legacy
// RLP or an EIP-2718 typed envelope). That encoding carries no sender field,
// so the sender is whatever the signature recovers to and a peer cannot assert
// one; the pool recovers it during validation.
func (s *Service) txSubscriber(ctx context.Context, _ peer.ID, data any) error {
	raw, ok := data.(*rawSSZBytes)
	if !ok {
		return nil
	}

	tx, err := transaction.DecodeEthereumTransaction(raw.data)
	if err != nil {
		log.Warn("Failed to decode gossiped transaction", "err", err)
		return nil // don't return error to avoid penalizing peer
	}

	errs := s.cfg.txPool.AddRemotes([]*transaction.Transaction{tx})
	if errs != nil && errs[0] != nil {
		log.Debug("Gossiped transaction rejected by pool", "hash", tx.Hash(), "err", errs[0])
		return nil
	}
	if n := txGossipReceived.Add(1); n == 1 || n%500 == 0 {
		log.Info("tx gossip: receiving", "received", n)
	}
	return nil
}
