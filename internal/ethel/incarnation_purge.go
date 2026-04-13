// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Incarnation map cleanup helpers. DetectIncarnationResidual reports how
// many entries are still present in modules.IncarnationMap at startup so
// operators can tell whether a database was written by older code or by
// an N42-native mode that still relied on the Erigon incarnation field.
// PurgeIncarnationMap removes every entry and is idempotent. eth-el sync
// does not use the map at all; the Reth-style account layout treats
// selfdestructed contracts as a batch delete of their storage slots.

package ethel

import (
	"context"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// countBucket returns the number of entries in a bucket using StatDBI (O(1)),
// not a full cursor scan. Returns 0 if the table is missing.
func countBucket(tx kv.Tx, name string) (uint64, error) {
	cursor, err := tx.Cursor(name)
	if err != nil {
		return 0, nil
	}
	defer cursor.Close()
	return cursor.Count()
}

// DetectIncarnationResidual returns the number of entries in IncarnationMap.
// Use at startup to surface stale data left by older code or N42 native runs.
func DetectIncarnationResidual(ctx context.Context, db kv.RoDB) (uint64, error) {
	tx, err := db.BeginRo(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return countBucket(tx, modules.IncarnationMap)
}

// PurgeIncarnationMap clears IncarnationMap and returns the number of entries
// removed. Safe to call repeatedly; idempotent on an empty table.
func PurgeIncarnationMap(ctx context.Context, db kv.RwDB) (uint64, error) {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count, err := countBucket(tx, modules.IncarnationMap)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if err := tx.ClearBucket(modules.IncarnationMap); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
