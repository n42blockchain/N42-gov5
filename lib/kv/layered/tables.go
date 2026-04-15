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
// Table classification for the layered two-DB split. Declares the
// hotTables, coldTables and cachedTables sets and exposes them via
// IsHotTable, IsColdTable and IsCachedTable. Hot tables (Account,
// Storage, Code, PlainCodeHash, IncarnationMap) live in the compact
// state DB and are cached in memory; cold tables (AccountChangeSet,
// StorageChangeSet, histories, receipts, logs, call traces) live in
// the append-heavy history DB. Any unclassified table falls through
// to the state DB so newly introduced tables keep working unchanged.

package layered

// Table classification for the two-DB split.
//
// Hot tables contain current state that is read on every block execution.
// They benefit from smaller DB size (better OS page cache utilization),
// faster compaction, and an in-memory read cache.
//
// Cold tables contain historical data (changesets, indices, receipts, logs).
// They are mostly append-only and read only for historical queries.

// hotTables are stored in the state DB (small, fast, cached).
var hotTables = map[string]struct{}{
	"Account": {},
	"Storage": {},
	"Code":    {},
}

// coldTables are stored in the history DB (large, append-heavy).
var coldTables = map[string]struct{}{
	"AccountChangeSet": {},
	"StorageChangeSet": {},
	"AccountHistory":   {},
	"StorageHistory":   {},
	"Receipt":          {},
	"TransactionLog":   {},
	"LogTopicIndex":    {},
	"LogAddressIndex":  {},
	"CallTraceSet":     {},
	"CallFromIndex":    {},
	"CallToIndex":      {},
}

// cachedTables are hot tables that benefit from the read cache.
var cachedTables = map[string]struct{}{
	"Account": {},
	"Storage": {},
	"Code":    {},
}

// IsHotTable returns true if the table should be stored in the state DB.
func IsHotTable(table string) bool {
	_, ok := hotTables[table]
	return ok
}

// IsColdTable returns true if the table should be stored in the history DB.
func IsColdTable(table string) bool {
	_, ok := coldTables[table]
	return ok
}

// IsCachedTable returns true if reads from this table should be cached.
func IsCachedTable(table string) bool {
	_, ok := cachedTables[table]
	return ok
}
