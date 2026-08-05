// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Batched transaction gossip wire format.
//
// One gossip message per transaction was the publish-path syscall bill: at
// ~11k tx/s each node re-encrypts and WSASends ~5 messages per transaction
// (mesh fan-out minus dedup), which profiled at ~24% of saturated-fleet CPU in
// network writes alone. Batching N transactions into one message divides the
// per-message costs (noise encryption, yamux frame, syscall, pubsub
// validation, dedup bookkeeping) by N without changing what is transported.
//
// The batch also restores the pool's parallel sender pre-warm: AddRemotes on a
// one-transaction slice recovers its sender inline, while a batch fans the
// recoveries out across workers (measured 9.1x on 512).

package sync

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/rlp"
)

// txBatchMarker prefixes a batched transaction message. It cannot collide with
// a single transaction's first byte: an EIP-2718 typed envelope currently uses
// types 0x01-0x04 (the spec reserves [0, 0x7f], but this is an internal gossip
// framing on a version-locked fleet, not a consensus format), and a legacy RLP
// transaction always starts >= 0xc0. Chosen near the top of the reserved range
// to stay out of the way of future public types for as long as possible.
const txBatchMarker byte = 0x7e

// Batch shape limits. flushBatchTxs bounds how many transactions one message
// carries; txBatchMaxBytes bounds the encoded payload so a batch stays well
// under the gossip message cap whatever the transactions look like.
const (
	txBatchMaxTxs   = 256
	txBatchMaxBytes = 256 * 1024
)

// errNotABatch reports a payload that does not start with txBatchMarker.
var errNotABatch = errors.New("not a transaction batch")

// encodeTxBatch frames already-encoded transactions as one batch payload:
// txBatchMarker || rlp([rawTx, rawTx, ...]).
func encodeTxBatch(rawTxs [][]byte) ([]byte, error) {
	body, err := rlp.EncodeToBytes(rawTxs)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(body))
	out = append(out, txBatchMarker)
	out = append(out, body...)
	return out, nil
}

// decodeTxBatch is the inverse of encodeTxBatch. A payload without the marker
// returns errNotABatch so the caller can fall through to the single-transaction
// path (older peers publish that during a rolling upgrade). Anything after the
// marker that fails to parse is an error: nothing else legitimately starts
// with the marker byte.
func decodeTxBatch(payload []byte) ([]*transaction.Transaction, error) {
	if len(payload) == 0 || payload[0] != txBatchMarker {
		return nil, errNotABatch
	}
	var rawTxs [][]byte
	if err := rlp.DecodeBytes(payload[1:], &rawTxs); err != nil {
		return nil, fmt.Errorf("batch envelope: %w", err)
	}
	if len(rawTxs) == 0 {
		return nil, errors.New("empty batch")
	}
	if len(rawTxs) > txBatchMaxTxs {
		return nil, fmt.Errorf("batch of %d exceeds the %d cap", len(rawTxs), txBatchMaxTxs)
	}
	txs := make([]*transaction.Transaction, 0, len(rawTxs))
	for i, raw := range rawTxs {
		tx, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			return nil, fmt.Errorf("batch item %d: %w", i, err)
		}
		txs = append(txs, tx)
	}
	return txs, nil
}
