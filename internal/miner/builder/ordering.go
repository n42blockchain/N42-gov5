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
// Transaction ordering heap for the local block builder. Implements a
// container/heap-based priority queue keyed on effective miner tip
// (uint256) while respecting per-sender nonce order. Produces the
// iterator consumed by the worker when assembling the next block's
// transaction list.

package builder

import (
	"container/heap"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TxByPriceAndNonce implements a transaction ordering where transactions
// from the same account are ordered by nonce, and across accounts ordered
// by effective gas tip (highest first). This is the standard Ethereum
// transaction ordering used for block building.
type TxByPriceAndNonce struct {
	txs     map[types.Address][]*transaction.Transaction // Per-account nonce-sorted txs
	heads   txsByPrice                                   // Heap of next tx per account (by price)
	signer  transaction.Signer                           // Used if From() is nil
	baseFee *uint256.Int                                 // Current block base fee
}

// NewTxByPriceAndNonce creates a transaction set sorted by effective tip.
// Each account's transactions must already be sorted by nonce internally
// (as returned by txpool.Pending).
func NewTxByPriceAndNonce(pending map[types.Address][]*transaction.Transaction, baseFee *uint256.Int) *TxByPriceAndNonce {
	t := &TxByPriceAndNonce{
		txs:     pending,
		baseFee: baseFee,
	}

	t.heads = make(txsByPrice, 0, len(pending))
	for from, accTxs := range pending {
		if len(accTxs) == 0 {
			continue
		}
		wrapped := &txWithPrice{
			tx:      accTxs[0],
			from:    from,
			baseFee: baseFee,
		}
		wrapped.computeEffectiveTip()
		t.heads = append(t.heads, wrapped)
		t.txs[from] = accTxs[1:]
	}
	heap.Init(&t.heads)
	return t
}

// Peek returns the next transaction by price without removing it.
func (t *TxByPriceAndNonce) Peek() *transaction.Transaction {
	if len(t.heads) == 0 {
		return nil
	}
	return t.heads[0].tx
}

// Shift replaces the current best head with the next transaction from the
// same account, re-sorting the heap.
func (t *TxByPriceAndNonce) Shift() {
	from := t.heads[0].from
	if txs, ok := t.txs[from]; ok && len(txs) > 0 {
		wrapped := &txWithPrice{
			tx:      txs[0],
			from:    from,
			baseFee: t.baseFee,
		}
		wrapped.computeEffectiveTip()
		t.heads[0] = wrapped
		t.txs[from] = txs[1:]
		heap.Fix(&t.heads, 0)
	} else {
		heap.Pop(&t.heads)
	}
}

// Pop removes the current best transaction and advances to the next
// transaction from a different account if available.
func (t *TxByPriceAndNonce) Pop() {
	heap.Pop(&t.heads)
}

// txWithPrice wraps a transaction with its computed effective tip for sorting.
// effectiveTip is stored as uint256.Int (stack-friendly, no heap alloc).
type txWithPrice struct {
	tx           *transaction.Transaction
	from         types.Address
	baseFee      *uint256.Int
	effectiveTip uint256.Int
	blobFeeCap   uint256.Int
}

func (t *txWithPrice) computeEffectiveTip() {
	tipCap := t.tx.GasTipCap()
	feeCap := t.tx.GasFeeCap()
	if blobFeeCap := t.tx.BlobFeeCap(); blobFeeCap != nil {
		t.blobFeeCap.Set(blobFeeCap)
	}
	if tipCap == nil {
		tipCap = t.tx.GasPrice()
	}
	if feeCap == nil {
		feeCap = t.tx.GasPrice()
	}
	if t.baseFee == nil || t.baseFee.IsZero() {
		t.effectiveTip.Set(tipCap)
		return
	}
	// effectiveTip = min(gasTipCap, gasFeeCap - baseFee)
	if feeCap.Cmp(t.baseFee) <= 0 {
		t.effectiveTip.Clear()
		return
	}
	t.effectiveTip.Sub(feeCap, t.baseFee) // diff = feeCap - baseFee
	if t.effectiveTip.Cmp(tipCap) > 0 {
		t.effectiveTip.Set(tipCap) // min(diff, tipCap)
	}
}

// txsByPrice implements heap.Interface for transactions ordered by effective tip.
type txsByPrice []*txWithPrice

func (s txsByPrice) Len() int { return len(s) }
func (s txsByPrice) Less(i, j int) bool {
	if cmp := s[i].effectiveTip.Cmp(&s[j].effectiveTip); cmp != 0 {
		return cmp > 0 // Higher execution-gas tip first.
	}
	// Blob transactions with the same execution-gas tip are ordered by their
	// blob fee cap. Apart from making the order deterministic for blob-heavy
	// blocks, this avoids filling the block with lower-fee single-blob
	// transactions before a higher-fee multi-blob transaction can fit.
	return s[i].blobFeeCap.Cmp(&s[j].blobFeeCap) > 0
}
func (s txsByPrice) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s *txsByPrice) Push(x interface{}) {
	*s = append(*s, x.(*txWithPrice))
}

func (s *txsByPrice) Pop() interface{} {
	old := *s
	n := len(old)
	x := old[n-1]
	old[n-1] = nil
	*s = old[:n-1]
	return x
}
