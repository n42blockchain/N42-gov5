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
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// flushToDB persists all local and pending transactions to the database.
// Called during graceful shutdown so transactions survive node restart.
//
// Storage format: PoolTransaction table, key = txHash, value = proto-encoded tx.
func (pool *TxsPool) flushToDB() error {
	db := pool.bc.DB()
	if db == nil {
		return nil
	}

	pool.mu.RLock()
	// Collect all transactions worth persisting:
	// 1. All local transactions (pending + queued)
	// 2. All pending transactions (they're executable and likely to be mined soon)
	txs := make(map[types.Hash]*transaction.Transaction)

	// Local transactions (both pending and queued).
	for addr := range pool.locals.accounts {
		if list, ok := pool.pending[addr]; ok {
			for _, tx := range list.Flatten() {
				txs[tx.Hash()] = tx
			}
		}
		if list, ok := pool.queue[addr]; ok {
			for _, tx := range list.Flatten() {
				txs[tx.Hash()] = tx
			}
		}
	}

	// All pending transactions (remote included — they're ready to execute).
	for _, list := range pool.pending {
		for _, tx := range list.Flatten() {
			txs[tx.Hash()] = tx
		}
	}

	// Pre-marshal all transactions while still holding the read lock to avoid
	// any race on the Transaction objects.
	type txEntry struct {
		hash types.Hash
		data []byte
	}
	entries := make([]txEntry, 0, len(txs))
	for hash, tx := range txs {
		data, err := tx.Marshal()
		if err != nil {
			log.Warn("Failed to marshal tx for persistence", "hash", hash, "err", err)
			continue
		}
		entries = append(entries, txEntry{hash: hash, data: data})
	}
	pool.mu.RUnlock()

	if len(entries) == 0 {
		return nil
	}

	err := db.Update(pool.ctx, func(dbTx kv.RwTx) error {
		// Clear old entries first.
		if err := dbTx.ClearBucket(kv.PoolTransaction); err != nil {
			return err
		}

		for _, e := range entries {
			if err := dbTx.Put(kv.PoolTransaction, e.hash.Bytes(), e.data); err != nil {
				return err
			}
		}
		log.Info("Transaction pool persisted", "txs", len(entries))
		return nil
	})

	return err
}

// loadFromDB restores persisted transactions from the database.
// Called during pool initialization to recover transactions from a previous session.
func (pool *TxsPool) loadFromDB() int {
	db := pool.bc.DB()
	if db == nil {
		return 0
	}

	var txs []*transaction.Transaction

	err := db.View(pool.ctx, func(dbTx kv.Tx) error {
		cursor, err := dbTx.Cursor(kv.PoolTransaction)
		if err != nil {
			return err
		}
		defer cursor.Close()

		for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				return err
			}
			var tx transaction.Transaction
			if err := tx.Unmarshal(v); err != nil {
				log.Warn("Failed to unmarshal persisted tx", "err", err)
				continue
			}
			txs = append(txs, &tx)
		}
		return nil
	})

	if err != nil {
		log.Warn("Failed to load persisted transactions", "err", err)
		return 0
	}

	if len(txs) == 0 {
		return 0
	}

	// Re-add transactions. Use AddLocals to mark them as local
	// (they were worth persisting, so treat as local for priority).
	errs := pool.addTxs(txs, true, false)
	loaded := 0
	for _, err := range errs {
		if err == nil {
			loaded++
		}
	}

	log.Info("Transaction pool loaded from disk", "total", len(txs), "accepted", loaded)

	// Clean up persisted data (will be re-persisted on next shutdown).
	_ = db.Update(pool.ctx, func(dbTx kv.RwTx) error {
		return dbTx.ClearBucket(kv.PoolTransaction)
	})

	return loaded
}
