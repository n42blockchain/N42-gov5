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
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

type persistedJournalScan struct {
	txs            []*transaction.Transaction
	totalEntries   int
	invalidEntries int
}

// flushToDB persists all local and pending transactions to the database.
// Called during graceful shutdown so transactions survive node restart.
//
// Storage format: TxPoolJournal table, key = txHash, value = proto-encoded tx.
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
		if err := dbTx.ClearBucket(modules.TxPoolJournal); err != nil {
			return err
		}

		for _, e := range entries {
			if err := dbTx.Put(modules.TxPoolJournal, e.hash.Bytes(), e.data); err != nil {
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
	txs, err := loadPersistedTransactions(pool.ctx, pool.bc.DB())
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

	return loaded
}

func loadPersistedTransactions(ctx context.Context, db kv.RwDB) ([]*transaction.Transaction, error) {
	if db == nil {
		return nil, nil
	}

	var (
		current    persistedJournalScan
		legacy     persistedJournalScan
		legacyKeys [][]byte
	)

	err := db.Update(ctx, func(dbTx kv.RwTx) error {
		hasCurrent, err := dbTx.ExistsBucket(modules.TxPoolJournal)
		if err != nil {
			return err
		}
		if hasCurrent {
			current, err = scanPersistedJournalTable(dbTx, modules.TxPoolJournal)
			if err != nil {
				return err
			}
		}

		hasLegacy, err := dbTx.ExistsBucket(kv.PoolTransaction)
		if err != nil {
			return err
		}
		if hasLegacy {
			legacy, legacyKeys, err = scanLegacyPoolTransactionTable(dbTx)
			if err != nil {
				return err
			}
		}

		if current.totalEntries > 0 {
			if err := dbTx.ClearBucket(modules.TxPoolJournal); err != nil {
				return err
			}
		}
		for _, key := range legacyKeys {
			if err := dbTx.Delete(kv.PoolTransaction, key); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load persisted tx journal: %w", err)
	}

	if current.invalidEntries > 0 {
		log.Warn("Dropped unreadable persisted tx journal entries", "invalid", current.invalidEntries, "total", current.totalEntries)
	}
	if len(legacyKeys) > 0 {
		log.Info("Migrated legacy persisted tx journal entries", "count", len(legacyKeys))
	}

	merged := make(map[types.Hash]*transaction.Transaction, len(current.txs)+len(legacy.txs))
	for _, tx := range current.txs {
		merged[tx.Hash()] = tx
	}
	for _, tx := range legacy.txs {
		merged[tx.Hash()] = tx
	}

	txs := make([]*transaction.Transaction, 0, len(merged))
	for _, tx := range merged {
		txs = append(txs, tx)
	}
	return txs, nil
}

func scanPersistedJournalTable(dbTx kv.Tx, table string) (scan persistedJournalScan, err error) {
	cursor, err := dbTx.Cursor(table)
	if err != nil {
		return scan, err
	}
	defer cursor.Close()

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return scan, err
		}
		scan.totalEntries++
		tx, err := decodePersistedTransactionEntry(k, v)
		if err != nil {
			scan.invalidEntries++
			continue
		}
		scan.txs = append(scan.txs, tx)
	}

	return scan, nil
}

func scanLegacyPoolTransactionTable(dbTx kv.Tx) (scan persistedJournalScan, keys [][]byte, err error) {
	cursor, err := dbTx.Cursor(kv.PoolTransaction)
	if err != nil {
		return scan, nil, err
	}
	defer cursor.Close()

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return scan, nil, err
		}
		scan.totalEntries++
		tx, err := decodePersistedTransactionEntry(k, v)
		if err != nil {
			continue
		}
		scan.txs = append(scan.txs, tx)
		keys = append(keys, bytes.Clone(k))
	}

	return scan, keys, nil
}

func decodePersistedTransactionEntry(key, value []byte) (*transaction.Transaction, error) {
	if len(key) != types.HashLength {
		return nil, fmt.Errorf("invalid persisted tx key length %d", len(key))
	}

	var expectedHash types.Hash
	if err := expectedHash.SetBytes(key); err != nil {
		return nil, err
	}

	var tx transaction.Transaction
	if err := tx.Unmarshal(value); err != nil {
		return nil, err
	}
	if tx.Hash() != expectedHash {
		return nil, fmt.Errorf("persisted tx hash mismatch")
	}

	return &tx, nil
}
