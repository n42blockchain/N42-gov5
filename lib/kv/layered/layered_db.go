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

package layered

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/n42blockchain/N42/lib/kv"
)

// LayeredDB implements kv.RwDB by splitting tables across two underlying
// MDBX databases: a compact state DB for hot data (accounts, storage, code)
// and a larger history DB for cold data (changesets, indices, logs).
//
// Tables not explicitly classified as cold are routed to the state DB,
// ensuring backward compatibility — any new tables automatically go to
// the state DB unless explicitly marked cold.
//
// A ShardedCache sits in front of hot table reads to accelerate the
// critical path during block execution.
type LayeredDB struct {
	stateDB   kv.RwDB
	historyDB kv.RwDB
	cache     *ShardedCache
	cfg       *Config
}

// NewLayeredDB creates a LayeredDB wrapping two MDBX instances.
// stateDB handles hot tables; historyDB handles cold tables.
func NewLayeredDB(stateDB, historyDB kv.RwDB, cfg *Config) *LayeredDB {
	cache := NewShardedCache(cfg.CacheShards, cfg.CacheCapacity)
	return &LayeredDB{
		stateDB:   stateDB,
		historyDB: historyDB,
		cache:     cache,
		cfg:       cfg,
	}
}

// dbFor returns the appropriate underlying DB for the given table.
func (db *LayeredDB) dbFor(table string) kv.RwDB {
	if IsColdTable(table) {
		return db.historyDB
	}
	return db.stateDB
}

// Cache returns the shared read cache (for testing/metrics).
func (db *LayeredDB) Cache() *ShardedCache {
	return db.cache
}

// StateDB returns the underlying state DB (for direct access when needed).
func (db *LayeredDB) StateDB() kv.RwDB {
	return db.stateDB
}

// HistoryDB returns the underlying history DB.
func (db *LayeredDB) HistoryDB() kv.RwDB {
	return db.historyDB
}

// --- kv.RoDB interface ---

func (db *LayeredDB) ReadOnly() bool {
	return db.stateDB.ReadOnly()
}

func (db *LayeredDB) AllTables() kv.TableCfg {
	// Merge table configs from both DBs.
	result := make(kv.TableCfg)
	for name, cfg := range db.stateDB.AllTables() {
		result[name] = cfg
	}
	for name, cfg := range db.historyDB.AllTables() {
		result[name] = cfg
	}
	return result
}

func (db *LayeredDB) PageSize() uint64 {
	return db.stateDB.PageSize()
}

func (db *LayeredDB) CHandle() unsafe.Pointer {
	return db.stateDB.CHandle()
}

func (db *LayeredDB) BeginRo(ctx context.Context) (kv.Tx, error) {
	stateTx, err := db.stateDB.BeginRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("layered: state BeginRo: %w", err)
	}

	historyTx, err := db.historyDB.BeginRo(ctx)
	if err != nil {
		stateTx.Rollback()
		return nil, fmt.Errorf("layered: history BeginRo: %w", err)
	}

	return &layeredTx{
		stateTx:   stateTx,
		historyTx: historyTx,
		cache:     db.cache,
		ctx:       ctx,
	}, nil
}

func (db *LayeredDB) View(ctx context.Context, f func(tx kv.Tx) error) error {
	tx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return f(tx)
}

// --- kv.RwDB interface ---

func (db *LayeredDB) BeginRw(ctx context.Context) (kv.RwTx, error) {
	stateTx, err := db.stateDB.BeginRw(ctx)
	if err != nil {
		return nil, fmt.Errorf("layered: state BeginRw: %w", err)
	}

	historyTx, err := db.historyDB.BeginRw(ctx)
	if err != nil {
		stateTx.Rollback()
		return nil, fmt.Errorf("layered: history BeginRw: %w", err)
	}

	return &layeredRwTx{
		layeredTx: layeredTx{
			stateTx:   stateTx,
			historyTx: historyTx,
			cache:     db.cache,
			ctx:       ctx,
		},
		stateRwTx:   stateTx,
		historyRwTx: historyTx,
	}, nil
}

func (db *LayeredDB) BeginRwNosync(ctx context.Context) (kv.RwTx, error) {
	stateTx, err := db.stateDB.BeginRwNosync(ctx)
	if err != nil {
		return nil, fmt.Errorf("layered: state BeginRwNosync: %w", err)
	}

	historyTx, err := db.historyDB.BeginRwNosync(ctx)
	if err != nil {
		stateTx.Rollback()
		return nil, fmt.Errorf("layered: history BeginRwNosync: %w", err)
	}

	return &layeredRwTx{
		layeredTx: layeredTx{
			stateTx:   stateTx,
			historyTx: historyTx,
			cache:     db.cache,
			ctx:       ctx,
		},
		stateRwTx:   stateTx,
		historyRwTx: historyTx,
	}, nil
}

func (db *LayeredDB) Update(ctx context.Context, f func(tx kv.RwTx) error) error {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *LayeredDB) UpdateNosync(ctx context.Context, f func(tx kv.RwTx) error) error {
	tx, err := db.BeginRwNosync(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *LayeredDB) Close() {
	db.cache.Clear()
	db.stateDB.Close()
	db.historyDB.Close()
}
