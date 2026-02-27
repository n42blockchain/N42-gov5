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

package txspool

import (
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/prque"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// promoteTx adds a transaction to the pending (processable) list of transactions
// and returns whether it was inserted or an older was better.
//
// Note, this method assumes the pool lock is held!
func (pool *TxsPool) promoteTx(addr types.Address, hash types.Hash, tx *transaction.Transaction) bool {
	if pool.pending[addr] == nil {
		pool.pending[addr] = newTxsList(true)
	}
	list := pool.pending[addr]

	inserted, old := list.Add(tx, pool.config.PriceBump)
	if !inserted {
		pool.all.Remove(hash)
		pool.priced.Removed(1)
		return false
	}
	if old != nil {
		pool.all.Remove(old.Hash())
		pool.priced.Removed(1)
	} else {
		pendingGauge.Inc()
	}
	pool.pendingNonces.set(addr, tx.Nonce()+1)
	pool.beats[addr] = time.Now()
	return true
}

// enqueueTx inserts a new transaction into the non-executable transaction queue.
//
// Note, this method assumes the pool lock is held!
func (pool *TxsPool) enqueueTx(hash types.Hash, tx *transaction.Transaction, local bool, addAll bool) (bool, error) {
	fromPtr := tx.From()
	if fromPtr == nil {
		return false, ErrInvalidSender
	}
	from := *fromPtr

	if pool.queue[from] == nil {
		pool.queue[from] = newTxsList(false)
	}
	inserted, old := pool.queue[from].Add(tx, pool.config.PriceBump)
	if !inserted {
		return false, ErrReplaceUnderpriced
	}
	if old != nil {
		pool.all.Remove(old.Hash())
		pool.priced.Removed(1)
	} else {
		queuedGauge.Inc()
	}

	if pool.all.Get(hash) == nil && !addAll {
		log.Error("Missing transaction in lookup set, please report the issue", "hash", hash)
	}
	if addAll {
		pool.all.Add(tx, local)
		pool.priced.Put(tx, local)
	}
	if _, exist := pool.beats[from]; !exist {
		pool.beats[from] = time.Now()
	}
	return old != nil, nil
}

// promoteExecutables moves transactions that have become processable from the
// future queue to the set of pending transactions. During this process, all
// invalidated transactions (low nonce, low balance) are deleted.
func (pool *TxsPool) promoteExecutables(accounts []types.Address) []*transaction.Transaction {
	var promoted []*transaction.Transaction

	for _, addr := range accounts {
		list := pool.queue[addr]
		if list == nil {
			continue
		}
		// Drop all transactions that are deemed too old (low nonce)
		forwards := list.Forward(pool.currentState.GetNonce(addr))
		for _, tx := range forwards {
			pool.all.Remove(tx.Hash())
		}
		// Drop all transactions that are too costly (low balance or out of gas)
		drops, _ := list.Filter(*pool.currentState.GetBalance(addr), pool.currentMaxGas)
		for _, tx := range drops {
			pool.all.Remove(tx.Hash())
		}

		// Gather all executable transactions and promote them
		readies := list.Ready(pool.pendingNonces.get(addr))
		for _, tx := range readies {
			if pool.promoteTx(addr, tx.Hash(), tx) {
				promoted = append(promoted, tx)
			}
		}
		queuedGauge.Add(-len(readies))

		// Drop all transactions over the allowed limit
		var caps []*transaction.Transaction
		if !pool.locals.contains(addr) {
			caps = list.Cap(int(pool.config.AccountQueue))
			for _, tx := range caps {
				pool.all.Remove(tx.Hash())
			}
		}
		pool.priced.Removed(len(caps))
		queuedGauge.Add(-(len(forwards) + len(drops) + len(caps)))
		if pool.locals.contains(addr) {
			localGauge.Add(-(len(forwards) + len(drops) + len(caps)))
		}
		if list.Empty() {
			delete(pool.queue, addr)
			delete(pool.beats, addr)
		}
	}
	return promoted
}

// truncatePending removes transactions from the pending queue if the pool is above the
// pending limit. The algorithm tries to reduce transaction counts by an approximately
// equal number for all accounts with many pending transactions.
func (pool *TxsPool) truncatePending() {
	pending := uint64(0)
	for _, list := range pool.pending {
		pending += uint64(list.Len())
	}
	if pending <= pool.config.GlobalSlots {
		return
	}

	// Assemble a spam order to penalize large transactors first
	spammers := prque.New(nil)
	for addr, list := range pool.pending {
		if !pool.locals.contains(addr) && uint64(list.Len()) > pool.config.AccountSlots {
			spammers.Push(addr, int64(list.Len()))
		}
	}

	// Gradually drop transactions from offenders
	offenders := []types.Address{}
	for pending > pool.config.GlobalSlots && !spammers.Empty() {
		offender, _ := spammers.Pop()
		offenders = append(offenders, offender.(types.Address))

		if len(offenders) > 1 {
			threshold := pool.pending[offender.(types.Address)].Len()

			for pending > pool.config.GlobalSlots && pool.pending[offenders[len(offenders)-2]].Len() > threshold {
				for i := 0; i < len(offenders)-1; i++ {
					list := pool.pending[offenders[i]]
					caps := list.Cap(list.Len() - 1)
					for _, tx := range caps {
						pool.all.Remove(tx.Hash())
						pool.pendingNonces.setIfLower(offenders[i], tx.Nonce())
						log.Debug("Removed fairness-exceeding pending transaction", "hash", tx.Hash())
					}
					pool.priced.Removed(len(caps))
					pendingGauge.Add(-len(caps))
					if pool.locals.contains(offenders[i]) {
						localGauge.Add(-len(caps))
					}
					pending--
				}
			}
		}
	}

	// If still above threshold, reduce to limit or min allowance
	if pending > pool.config.GlobalSlots && len(offenders) > 0 {
		for pending > pool.config.GlobalSlots && uint64(pool.pending[offenders[len(offenders)-1]].Len()) > pool.config.AccountSlots {
			for _, addr := range offenders {
				list := pool.pending[addr]
				caps := list.Cap(list.Len() - 1)
				for _, tx := range caps {
					pool.all.Remove(tx.Hash())
					pool.pendingNonces.setIfLower(addr, tx.Nonce())
					log.Debug("Removed fairness-exceeding pending transaction", "hash", tx.Hash())
				}
				pool.priced.Removed(len(caps))
				pendingGauge.Add(-len(caps))
				if pool.locals.contains(addr) {
					localGauge.Add(-len(caps))
				}
				pending--
			}
		}
	}
}

// truncateQueue drops the oldest transactions in the queue if the pool is above the
// global queue limit.
func (pool *TxsPool) truncateQueue() {
	queued := uint64(0)
	for _, list := range pool.queue {
		queued += uint64(list.Len())
	}
	if queued <= pool.config.GlobalQueue {
		return
	}

	// Sort all accounts with queued transactions by heartbeat
	addresses := make(addressesByHeartbeat, 0, len(pool.queue))
	for addr := range pool.queue {
		if !pool.locals.contains(addr) {
			addresses = append(addresses, addressByHeartbeat{addr, pool.beats[addr]})
		}
	}
	sort.Sort(addresses)

	// Drop transactions until the total is below the limit or only locals remain
	for drop := queued - pool.config.GlobalQueue; drop > 0 && len(addresses) > 0; {
		addr := addresses[len(addresses)-1]
		list := pool.queue[addr.address]
		addresses = addresses[:len(addresses)-1]

		if size := uint64(list.Len()); size <= drop {
			for _, tx := range list.Flatten() {
				pool.removeTx(tx.Hash(), true)
			}
			drop -= size
			continue
		}
		txs := list.Flatten()
		for i := len(txs) - 1; i >= 0 && drop > 0; i-- {
			pool.removeTx(txs[i].Hash(), true)
			drop--
		}
	}
}

// demoteUnexecutables removes invalid and processed transactions from the pools
// executable/pending queue and any subsequent transactions that become unexecutable
// are moved back into the future queue.
//
// Note: transactions are not marked as removed in the priced list because re-heaping
// is always explicitly triggered by SetBaseFee and it would be unnecessary and wasteful
// to trigger a re-heap in this function.
func (pool *TxsPool) demoteUnexecutables() {
	for addr, list := range pool.pending {
		nonce := pool.currentState.GetNonce(addr)

		// Drop all transactions that are deemed too old (low nonce)
		olds := list.Forward(nonce)
		for _, tx := range olds {
			pool.all.Remove(tx.Hash())
		}
		// Drop all transactions that are too costly, and queue any invalids back for later
		drops, invalids := list.Filter(*pool.currentState.GetBalance(addr), pool.currentMaxGas)
		for _, tx := range drops {
			pool.all.Remove(tx.Hash())
		}
		for _, tx := range invalids {
			pool.enqueueTx(tx.Hash(), tx, false, false)
		}
		pendingGauge.Add(-(len(olds) + len(drops) + len(invalids)))
		if pool.locals.contains(addr) {
			localGauge.Add(-(len(olds) + len(drops) + len(invalids)))
		}
		// If there's a gap in front, alert and postpone all transactions
		if list.Len() > 0 && list.txs.Get(nonce) == nil {
			gapped := list.Cap(0)
			for _, tx := range gapped {
				log.Error("Demoting invalidated transaction", "hash", tx.Hash())
				pool.enqueueTx(tx.Hash(), tx, false, false)
			}
			pendingGauge.Add(-len(gapped))
		}
		if list.Empty() {
			delete(pool.pending, addr)
		}
	}
}
