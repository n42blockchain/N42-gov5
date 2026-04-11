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
// WarmupOverlayCache pre-loads the layered ShardedCache on startup.
// Opens a read tx over modules.Account, walks up to maxWarmupEntries
// (100,000) key/value pairs and inserts them into the cache to reduce
// cold-start misses after a node restart. The blocks parameter is
// reserved for future range-limited warmup filtering.

package state

import (
	"context"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// maxWarmupEntries caps the number of entries loaded during warmup
// to bound startup time regardless of the blocks parameter.
const maxWarmupEntries = 100_000

// WarmupOverlayCache pre-loads recent state from the Account table into
// the ShardedCache, reducing cold-start cache misses after restart.
//
// It opens a read transaction on db, iterates Account entries, and
// inserts each key-value pair into the cache. The number of entries
// loaded is bounded by maxWarmupEntries to prevent excessive startup time.
//
// The blocks parameter is currently unused but reserved for future
// filtering by block range (e.g., only loading accounts touched in the
// last N blocks).
func WarmupOverlayCache(ctx context.Context, db kv.RoDB, cache *layered.ShardedCache, blocks int) error {
	start := time.Now()

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(modules.Account)
	if err != nil {
		return err
	}
	defer cursor.Close()

	count := 0
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}

		// Respect context cancellation.
		if count%1000 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		cache.Put(modules.Account, k, v)
		count++

		if count >= maxWarmupEntries {
			break
		}
	}

	elapsed := time.Since(start)
	log.Info("overlay cache warmup complete", "entries", count, "elapsed", elapsed)
	return nil
}
