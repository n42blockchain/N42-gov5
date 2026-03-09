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
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
)

// layeredTx implements kv.Tx by routing table operations to the appropriate
// underlying transaction (state or history).
type layeredTx struct {
	stateTx   kv.Tx
	historyTx kv.Tx
	cache     *ShardedCache
	ctx       context.Context
}

// txFor returns the appropriate underlying transaction for the given table.
func (tx *layeredTx) txFor(table string) kv.Tx {
	if IsColdTable(table) {
		return tx.historyTx
	}
	return tx.stateTx
}

// --- kv.StatelessReadTx ---

func (tx *layeredTx) GetOne(table string, key []byte) ([]byte, error) {
	// Check cache first for hot tables.
	if IsCachedTable(table) {
		if v, ok := tx.cache.Get(table, key); ok {
			return v, nil
		}
	}

	v, err := tx.txFor(table).GetOne(table, key)
	if err != nil {
		return nil, err
	}

	// Populate cache on read.
	if IsCachedTable(table) {
		tx.cache.Put(table, key, v)
	}

	return v, nil
}

func (tx *layeredTx) Has(table string, key []byte) (bool, error) {
	if IsCachedTable(table) {
		if v, ok := tx.cache.Get(table, key); ok {
			return v != nil, nil
		}
	}
	return tx.txFor(table).Has(table, key)
}

func (tx *layeredTx) ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error {
	return tx.txFor(table).ForEach(table, fromPrefix, walker)
}

func (tx *layeredTx) ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error {
	return tx.txFor(table).ForPrefix(table, prefix, walker)
}

func (tx *layeredTx) ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error {
	return tx.txFor(table).ForAmount(table, prefix, amount, walker)
}

func (tx *layeredTx) Commit() error {
	if err := tx.historyTx.Commit(); err != nil {
		return fmt.Errorf("layered: history commit: %w", err)
	}
	if err := tx.stateTx.Commit(); err != nil {
		return fmt.Errorf("layered: state commit: %w", err)
	}
	return nil
}

func (tx *layeredTx) Rollback() {
	tx.historyTx.Rollback()
	tx.stateTx.Rollback()
}

func (tx *layeredTx) ReadSequence(table string) (uint64, error) {
	return tx.txFor(table).ReadSequence(table)
}

// --- kv.Tx ---

func (tx *layeredTx) ViewID() uint64 {
	return tx.stateTx.ViewID()
}

func (tx *layeredTx) Cursor(table string) (kv.Cursor, error) {
	return tx.txFor(table).Cursor(table)
}

func (tx *layeredTx) CursorDupSort(table string) (kv.CursorDupSort, error) {
	return tx.txFor(table).CursorDupSort(table)
}

func (tx *layeredTx) DBSize() (uint64, error) {
	stateSize, err := tx.stateTx.DBSize()
	if err != nil {
		return 0, err
	}
	historySize, err := tx.historyTx.DBSize()
	if err != nil {
		return 0, err
	}
	return stateSize + historySize, nil
}

func (tx *layeredTx) Range(table string, fromPrefix, toPrefix []byte) (iter.KV, error) {
	return tx.txFor(table).Range(table, fromPrefix, toPrefix)
}

func (tx *layeredTx) RangeAscend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return tx.txFor(table).RangeAscend(table, fromPrefix, toPrefix, limit)
}

func (tx *layeredTx) RangeDescend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return tx.txFor(table).RangeDescend(table, fromPrefix, toPrefix, limit)
}

func (tx *layeredTx) Prefix(table string, prefix []byte) (iter.KV, error) {
	return tx.txFor(table).Prefix(table, prefix)
}

func (tx *layeredTx) RangeDupSort(table string, key []byte, fromPrefix, toPrefix []byte, asc order.By, limit int) (iter.KV, error) {
	return tx.txFor(table).RangeDupSort(table, key, fromPrefix, toPrefix, asc, limit)
}

func (tx *layeredTx) CHandle() unsafe.Pointer {
	return tx.stateTx.CHandle()
}

func (tx *layeredTx) BucketSize(table string) (uint64, error) {
	return tx.txFor(table).BucketSize(table)
}

// --- kv.BucketMigratorRO ---

func (tx *layeredTx) ListBuckets() ([]string, error) {
	stateBuckets, err := tx.stateTx.ListBuckets()
	if err != nil {
		return nil, err
	}
	historyBuckets, err := tx.historyTx.ListBuckets()
	if err != nil {
		return nil, err
	}
	return append(stateBuckets, historyBuckets...), nil
}

// cacheMutation represents a deferred cache update.
type cacheMutation struct {
	table  string
	key    string // string([]byte) for safe storage
	value  []byte // nil means delete
	isDel  bool
}

// layeredRwTx implements kv.RwTx by routing write operations to the
// appropriate underlying RW transaction and maintaining cache coherence.
//
// Cache updates are deferred until Commit succeeds, preventing stale
// cache entries on transaction rollback.
type layeredRwTx struct {
	layeredTx
	stateRwTx      kv.RwTx
	historyRwTx    kv.RwTx
	pendingCache   []cacheMutation
	clearCacheAll  bool
}

// rwTxFor returns the appropriate underlying RW transaction for the given table.
func (tx *layeredRwTx) rwTxFor(table string) kv.RwTx {
	if IsColdTable(table) {
		return tx.historyRwTx
	}
	return tx.stateRwTx
}

// --- kv.StatelessWriteTx ---

func (tx *layeredRwTx) Put(table string, k, v []byte) error {
	if err := tx.rwTxFor(table).Put(table, k, v); err != nil {
		return err
	}
	if IsCachedTable(table) {
		// Copy value for deferred cache update.
		var vc []byte
		if v != nil {
			vc = make([]byte, len(v))
			copy(vc, v)
		}
		tx.pendingCache = append(tx.pendingCache, cacheMutation{
			table: table,
			key:   string(k),
			value: vc,
		})
	}
	return nil
}

func (tx *layeredRwTx) Delete(table string, k []byte) error {
	if err := tx.rwTxFor(table).Delete(table, k); err != nil {
		return err
	}
	if IsCachedTable(table) {
		tx.pendingCache = append(tx.pendingCache, cacheMutation{
			table: table,
			key:   string(k),
			isDel: true,
		})
	}
	return nil
}

func (tx *layeredRwTx) IncrementSequence(table string, amount uint64) (uint64, error) {
	return tx.rwTxFor(table).IncrementSequence(table, amount)
}

func (tx *layeredRwTx) Append(table string, k, v []byte) error {
	if err := tx.rwTxFor(table).Append(table, k, v); err != nil {
		return err
	}
	if IsCachedTable(table) {
		var vc []byte
		if v != nil {
			vc = make([]byte, len(v))
			copy(vc, v)
		}
		tx.pendingCache = append(tx.pendingCache, cacheMutation{
			table: table,
			key:   string(k),
			value: vc,
		})
	}
	return nil
}

func (tx *layeredRwTx) AppendDup(table string, k, v []byte) error {
	return tx.rwTxFor(table).AppendDup(table, k, v)
}

// --- kv.RwTx ---

func (tx *layeredRwTx) RwCursor(table string) (kv.RwCursor, error) {
	return tx.rwTxFor(table).RwCursor(table)
}

func (tx *layeredRwTx) RwCursorDupSort(table string) (kv.RwCursorDupSort, error) {
	return tx.rwTxFor(table).RwCursorDupSort(table)
}

func (tx *layeredRwTx) CollectMetrics() {
	tx.stateRwTx.CollectMetrics()
	tx.cache.CollectMetrics()
}

func (tx *layeredRwTx) Commit() error {
	// Commit history first — it's append-only and recoverable.
	if err := tx.historyRwTx.Commit(); err != nil {
		return fmt.Errorf("layered: history commit: %w", err)
	}
	if err := tx.stateRwTx.Commit(); err != nil {
		return fmt.Errorf("layered: state commit: %w", err)
	}

	// Apply deferred cache mutations only after both commits succeed.
	if tx.clearCacheAll {
		tx.cache.Clear()
	}
	for _, m := range tx.pendingCache {
		if m.isDel {
			tx.cache.Delete(m.table, []byte(m.key))
		} else {
			tx.cache.Put(m.table, []byte(m.key), m.value)
		}
	}
	tx.pendingCache = nil
	return nil
}

func (tx *layeredRwTx) Rollback() {
	tx.historyRwTx.Rollback()
	tx.stateRwTx.Rollback()
	// Discard pending cache mutations — nothing was committed.
	tx.pendingCache = nil
}

// --- kv.BucketMigrator ---

func (tx *layeredRwTx) DropBucket(name string) error {
	return tx.rwTxFor(name).DropBucket(name)
}

func (tx *layeredRwTx) CreateBucket(name string) error {
	return tx.rwTxFor(name).CreateBucket(name)
}

func (tx *layeredRwTx) ExistsBucket(name string) (bool, error) {
	return tx.rwTxFor(name).ExistsBucket(name)
}

func (tx *layeredRwTx) ClearBucket(name string) error {
	if IsCachedTable(name) {
		tx.clearCacheAll = true
	}
	return tx.rwTxFor(name).ClearBucket(name)
}
