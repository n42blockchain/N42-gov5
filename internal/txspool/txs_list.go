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
// Per-account transaction list. Holds pending and queued
// transactions for a single sender ordered by nonce, with a
// container/heap view for cost/gasPrice-based eviction. Handles
// replacement on nonce collisions subject to a minimum price bump.

package txspool

import (
	"container/heap"
	"math"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// priceBumpDivisor avoids a heap allocation on every price-bump check.
var priceBumpDivisor = uint256.NewInt(100)

// --- txsList: per-account transaction list ---

// txsList is a "list" of transactions belonging to an account, sorted by nonce.
// The same type can be used for both the executable/pending queue (strict mode)
// and the non-executable/future queue.
type txsList struct {
	strict  bool
	txs     *txsSortedMap
	costcap uint256.Int
	gascap  uint64
}

func newTxsList(strict bool) *txsList {
	return &txsList{
		strict:  strict,
		txs:     newTxSortedMap(),
		costcap: *uint256.NewInt(0),
	}
}

// Overlaps returns whether the transaction specified has the same nonce as one
// already contained within the list.
func (l *txsList) Overlaps(tx *transaction.Transaction) bool {
	return l.txs.Get(tx.Nonce()) != nil
}

// Contains reports whether the list holds a transaction with the given nonce.
func (l *txsList) Contains(nonce uint64) bool {
	return l.txs.Get(nonce) != nil
}

// Add tries to insert a new transaction into the list, returning whether the
// transaction was accepted, and if yes, any previous transaction it replaced.
// All uint256 arithmetic uses stack-local variables to avoid heap allocation
// on the per-tx-add hot path.
func (l *txsList) Add(tx *transaction.Transaction, priceBump uint64) (bool, *transaction.Transaction) {
	old := l.txs.Get(tx.Nonce())
	if old != nil {
		if old.GasFeeCapCmp(tx) >= 1 || old.GasTipCapCmp(tx) >= 1 {
			return false, nil
		}
		// thresholdFeeCap = oldFC * (100 + priceBump) / 100
		var bump, threshold uint256.Int
		bump.SetUint64(100 + priceBump)
		threshold.Mul(&bump, old.GasFeeCap())
		threshold.Div(&threshold, priceBumpDivisor)
		if tx.GasFeeCapIntCmp(&threshold) < 0 {
			return false, nil
		}
		// thresholdTip = oldTip * (100 + priceBump) / 100
		bump.SetUint64(100 + priceBump)
		threshold.Mul(&bump, old.GasTipCap())
		threshold.Div(&threshold, priceBumpDivisor)
		if tx.GasTipCapIntCmp(&threshold) < 0 {
			return false, nil
		}
	}

	l.txs.Put(tx)

	var gasU, cost uint256.Int
	gasU.SetUint64(tx.Gas())
	cost.Mul(tx.GasPrice(), &gasU)
	cost.Add(&cost, tx.Value())
	if l.costcap.Cmp(&cost) < 0 {
		l.costcap = cost
	}
	if gas := tx.Gas(); l.gascap < gas {
		l.gascap = gas
	}
	return true, old
}

// Forward removes all transactions from the list with a nonce lower than the
// provided threshold.
func (l *txsList) Forward(threshold uint64) []*transaction.Transaction {
	return l.txs.Forward(threshold)
}

// Filter removes all transactions from the list with a cost or gas limit higher
// than the provided thresholds. Strict-mode invalidated transactions are also returned.
func (l *txsList) Filter(costLimit uint256.Int, gasLimit uint64) ([]*transaction.Transaction, []*transaction.Transaction) {
	if l.costcap.Cmp(&costLimit) <= 0 && l.gascap <= gasLimit {
		return nil, nil
	}
	l.costcap = costLimit
	l.gascap = gasLimit

	removed := l.txs.Filter(func(tx *transaction.Transaction) bool {
		cost := uint256.NewInt(0).Add(tx.Value(), uint256.NewInt(0).Mul(tx.GasPrice(), uint256.NewInt(tx.Gas())))
		return tx.Gas() > gasLimit || cost.Cmp(&costLimit) > 0
	})
	if len(removed) == 0 {
		return nil, nil
	}

	var invalids []*transaction.Transaction
	if l.strict {
		lowest := uint64(math.MaxUint64)
		for _, tx := range removed {
			if nonce := tx.Nonce(); lowest > nonce {
				lowest = nonce
			}
		}
		invalids = l.txs.filter(func(tx *transaction.Transaction) bool { return tx.Nonce() > lowest })
	}
	l.txs.reheap()
	return removed, invalids
}

// Cap places a hard limit on the number of items, returning all transactions
// exceeding that limit.
func (l *txsList) Cap(threshold int) []*transaction.Transaction {
	return l.txs.Cap(threshold)
}

// Remove deletes a transaction from the maintained list, returning whether the
// transaction was found, and also returning any transaction invalidated due to
// the deletion (strict mode only).
func (l *txsList) Remove(tx *transaction.Transaction) (bool, []*transaction.Transaction) {
	nonce := tx.Nonce()
	if removed := l.txs.Remove(nonce); !removed {
		return false, nil
	}
	if l.strict {
		return true, l.txs.Filter(func(tx *transaction.Transaction) bool { return tx.Nonce() > nonce })
	}
	return true, nil
}

// Ready retrieves a sequentially increasing list of transactions starting at the
// provided nonce that is ready for processing.
func (l *txsList) Ready(start uint64) []*transaction.Transaction {
	return l.txs.Ready(start)
}

func (l *txsList) Len() int    { return l.txs.Len() }
func (l *txsList) Empty() bool { return l.Len() == 0 }

func (l *txsList) Flatten() []*transaction.Transaction {
	return l.txs.Flatten()
}

// FlattenReadOnly returns the sorted tx slice without copying.
// Caller must not modify the returned slice.
func (l *txsList) FlattenReadOnly() []*transaction.Transaction {
	return l.txs.FlattenReadOnly()
}

// LastElement returns the transaction with the highest nonce, or nil if empty.
func (l *txsList) LastElement() *transaction.Transaction {
	if l.Empty() {
		return nil
	}
	return l.txs.LastElement()
}

// --- txPricedList: price-sorted heap for eviction ---

const (
	urgentRatio   = 4
	floatingRatio = 1
)

// txPricedList is a price-sorted heap to allow operating on transactions pool
// contents in a price-incrementing way. Only remote transactions are tracked.
type txPricedList struct {
	// Number of stale price points (re-heap trigger).
	// This field is accessed atomically, and must be the first field
	// to ensure correct alignment for atomic operations.
	stales int64

	all              *txLookup
	urgent, floating priceHeap
	reheapMu         sync.Mutex
}

func newTxPricedList(all *txLookup) *txPricedList {
	return &txPricedList{
		all: all,
	}
}

// Put inserts a new transaction into the heap. Local transactions are skipped.
func (l *txPricedList) Put(tx *transaction.Transaction, local bool) {
	if local {
		return
	}
	heap.Push(&l.urgent, tx)
}

// Removed notifies the priced list that an old transaction dropped from the pool.
func (l *txPricedList) Removed(count int) {
	stales := atomic.AddInt64(&l.stales, int64(count))
	if int(stales) <= (len(l.urgent.list)+len(l.floating.list))/4 {
		return
	}
	l.Reheap()
}

// Underpriced checks whether a transaction is cheaper than the lowest priced
// remote transaction currently being tracked.
func (l *txPricedList) Underpriced(tx *transaction.Transaction) bool {
	return (l.underpricedFor(&l.urgent, tx) || len(l.urgent.list) == 0) &&
		(l.underpricedFor(&l.floating, tx) || len(l.floating.list) == 0) &&
		(len(l.urgent.list) != 0 || len(l.floating.list) != 0)
}

func (l *txPricedList) underpricedFor(h *priceHeap, tx *transaction.Transaction) bool {
	for len(h.list) > 0 {
		if l.all.GetRemote(h.list[0].Hash()) == nil {
			atomic.AddInt64(&l.stales, -1)
			heap.Pop(h)
			continue
		}
		break
	}
	if len(h.list) == 0 {
		return false
	}
	return h.cmp(h.list[0], tx) >= 0
}

// discardPrealloc caps the initial capacity of Discard's result. Sizing it by
// the caller's `slots` instead reserved the pool's entire projected overflow on
// every single call: the caller passes
// `all.Slots() - capacity + numSlots(tx)`, which is hundreds of thousands of
// entries whenever the pool is over capacity, while the loop below usually
// breaks out after a handful of pops — or immediately, when the priced lists
// are empty. Big allocation, no work.
//
// Measured on the live fleet over 56 minutes at ~417 tx/s: this one make() was
// 2.84 TB of the node's 2.98 TB of total allocation (95.2%), about 2.1 MB per
// transaction admitted, and it is why the node spent ~40% of its CPU in the
// garbage collector — against 11-14% in all of MDBX and cryptography combined.
//
// The allocation should scale with the transactions actually dropped, which is
// what append does.
const discardPrealloc = 64

// Discard finds a number of most underpriced transactions, removes them from the
// priced list and returns them for further removal from the entire pool.
func (l *txPricedList) Discard(slots int, force bool) ([]*transaction.Transaction, bool) {
	prealloc := slots
	if prealloc > discardPrealloc {
		prealloc = discardPrealloc
	}
	if prealloc < 0 {
		// A caller that computes a negative overflow would otherwise panic in
		// make(); the loop is a no-op for it either way.
		prealloc = 0
	}
	drop := make([]*transaction.Transaction, 0, prealloc)
	for slots > 0 {
		if len(l.urgent.list)*floatingRatio > len(l.floating.list)*urgentRatio || floatingRatio == 0 {
			if len(l.urgent.list) == 0 {
				break
			}
			tx := heap.Pop(&l.urgent).(*transaction.Transaction)
			if l.all.GetRemote(tx.Hash()) == nil {
				atomic.AddInt64(&l.stales, -1)
				continue
			}
			heap.Push(&l.floating, tx)
		} else {
			if len(l.floating.list) == 0 {
				break
			}
			tx := heap.Pop(&l.floating).(*transaction.Transaction)
			if l.all.GetRemote(tx.Hash()) == nil {
				atomic.AddInt64(&l.stales, -1)
				continue
			}
			drop = append(drop, tx)
			slots -= numSlots(tx)
		}
	}
	if slots > 0 && !force {
		for _, tx := range drop {
			heap.Push(&l.urgent, tx)
		}
		return nil, false
	}
	return drop, true
}

// Reheap forcibly rebuilds the heap based on the current remote transaction set.
func (l *txPricedList) Reheap() {
	l.reheapMu.Lock()
	defer l.reheapMu.Unlock()

	atomic.StoreInt64(&l.stales, 0)
	l.urgent.list = make([]*transaction.Transaction, 0, l.all.RemoteCount())
	l.all.Range(func(hash types.Hash, tx *transaction.Transaction, local bool) bool {
		l.urgent.list = append(l.urgent.list, tx)
		return true
	}, false, true)
	heap.Init(&l.urgent)

	floatingCount := len(l.urgent.list) * floatingRatio / (urgentRatio + floatingRatio)
	l.floating.list = make([]*transaction.Transaction, floatingCount)
	for i := 0; i < floatingCount; i++ {
		l.floating.list[i] = heap.Pop(&l.urgent).(*transaction.Transaction)
	}
	heap.Init(&l.floating)
}

// SetBaseFee updates the base fee and triggers a re-heap.
func (l *txPricedList) SetBaseFee(baseFee *uint256.Int) {
	l.urgent.baseFee = baseFee
	l.Reheap()
}
