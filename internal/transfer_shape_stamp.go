// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Block transfer shape (r97a stage-0 stamps, always on). R1 (transfer lanes,
// run-import-chain-design.md) sizes its gain by how many of a block's
// senders are also recipients in the same block and by the share of plain
// value transfers. Two sources:
//
//   - per block, on the "parallel block" line: the fast-path, direct and
//     plan-path transfer counters' deltas across the wave (fastTx, directTx,
//     planTx). They are process-wide counters the workers already flush once
//     each; a concurrent build in the same process (the leader's) counts into
//     the same window, so on a leader they are an upper bound.
//   - sampled, on its own "transfer shape" line: every
//     transferShapeEvery-th full block (by number) gets one pass over its
//     transactions on a goroutine after its run, counting distinct senders,
//     senders with more than one transaction, distinct recipients, senders
//     that are also recipients and the transactions those senders send, and
//     the transactions shaped like a plain transfer (a recipient, no data, no
//     access or authorization list). It reads only each transaction's cached
//     sender, recipient and payload, after the run recovered every sender, so
//     it adds no work to the run chain; the pass costs a few ms of one core
//     on one block in transferShapeEvery.

// transferShapeEvery is the sampling period of the "transfer shape" line.
const transferShapeEvery = 8

// transferShapeMinTxs is the smallest block the shape is sampled on.
const transferShapeMinTxs = 1000

// blockTransferShape is one block's sender/recipient overlap.
type blockTransferShape struct {
	txs           int // transactions
	noSender      int // transactions without a cached sender (not counted below)
	plainShape    int // a recipient, no data, no access/authorization list
	senders       int // distinct senders
	multiSenders  int // senders with more than one transaction
	maxChain      int // the most transactions of one sender
	recipients    int // distinct recipients
	sharedSenders int // senders that are also recipients in the block
	sharedTxs     int // transactions sent by those senders
}

// measureTransferShape computes txs' shape. A sender is taken from the
// transaction's declared from, else from the memo the run's sender recovery
// left under signer (never recovered here).
func measureTransferShape(signer transaction.Signer, txs []*transaction.Transaction) blockTransferShape {
	s := blockTransferShape{txs: len(txs)}
	perSender := make(map[types.Address]int, len(txs)/16+16)
	recipients := make(map[types.Address]struct{}, len(txs)/4+16)
	for _, tx := range txs {
		if to := tx.To(); to != nil {
			recipients[*to] = struct{}{}
			if len(tx.Data()) == 0 && len(tx.AccessList()) == 0 && len(tx.AuthList()) == 0 {
				s.plainShape++
			}
		}
		var from types.Address
		if f := tx.From(); f != nil {
			from = *f
		} else if tx.HasSenderMemo(signer) {
			a, err := transaction.Sender(signer, tx)
			if err != nil {
				s.noSender++
				continue
			}
			from = a
		} else {
			s.noSender++
			continue
		}
		perSender[from]++
	}
	s.senders, s.recipients = len(perSender), len(recipients)
	for addr, n := range perSender {
		if n > 1 {
			s.multiSenders++
		}
		s.maxChain = max(s.maxChain, n)
		if _, ok := recipients[addr]; ok {
			s.sharedSenders++
			s.sharedTxs += n
		}
	}
	return s
}

// fields returns the shape's stamps.
func (s blockTransferShape) fields() []any {
	return []any{"txs", s.txs, "noSender", s.noSender, "plainShape", s.plainShape,
		"senders", s.senders, "multiSenders", s.multiSenders, "maxChain", s.maxChain,
		"recipients", s.recipients, "sharedSenders", s.sharedSenders, "sharedTxs", s.sharedTxs}
}

// sampleTransferShape logs the "transfer shape" line for block n on its own
// goroutine when n is a sampled full block.
func sampleTransferShape(n uint64, signer transaction.Signer, txs []*transaction.Transaction) {
	if n%transferShapeEvery != 0 || len(txs) < transferShapeMinTxs {
		return
	}
	go func() {
		s := measureTransferShape(signer, txs)
		log.Info("transfer shape", append([]any{"n", n}, s.fields()...)...)
	}()
}
