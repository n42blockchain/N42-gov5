// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// GossipSub subscriber for mempool transactions. txSubscriber decodes
// types_pb.Transaction, converts to the internal transaction.Transaction
// type and adds it to the txPool via AddRemotes, swallowing decode
// errors so misbehaving peers are not immediately penalised.

package sync

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
)

// txSubscriber handles incoming transaction messages from GossipSub.
// Each message contains a single SSZ-encoded Transaction.
func (s *Service) txSubscriber(ctx context.Context, msg proto.Message) error {
	pbTx, ok := msg.(*types_pb.Transaction)
	if !ok {
		return nil
	}

	tx, err := transaction.FromProtoMessage(pbTx)
	if err != nil {
		log.Debug("Failed to decode gossiped transaction", "err", err)
		return nil // don't return error to avoid penalizing peer
	}

	errs := s.cfg.txPool.AddRemotes([]*transaction.Transaction{tx})
	if errs != nil && errs[0] != nil {
		log.Debug("Gossiped transaction rejected by pool", "hash", tx.Hash(), "err", errs[0])
	}
	return nil
}
