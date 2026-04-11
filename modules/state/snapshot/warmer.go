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
// Warmer pre-populates the snapshot cache on startup.
// DefaultWarmupAccounts (500000) caps the number of modules.Account
// entries scanned and inserted into the layered.ShardedCache so the
// first blocks after a restart hit warm memory instead of MDBX. A
// zero or negative maxAccounts falls back to the default.

package snapshot

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// DefaultWarmupAccounts is the default number of accounts to pre-load.
const DefaultWarmupAccounts = 500_000

// Warmer pre-populates the ShardedCache from MDBX on startup.
// It scans the Account table and loads up to maxAccounts entries into the
// cache, improving read performance for the first blocks after a restart.
type Warmer struct {
	db          kv.RwDB
	cache       *layered.ShardedCache
	maxAccounts int
}

// NewWarmer creates a new cache warmer.
// If maxAccounts <= 0, DefaultWarmupAccounts is used.
func NewWarmer(db kv.RwDB, cache *layered.ShardedCache, maxAccounts int) *Warmer {
	if maxAccounts <= 0 {
		maxAccounts = DefaultWarmupAccounts
	}
	return &Warmer{
		db:          db,
		cache:       cache,
		maxAccounts: maxAccounts,
	}
}

// Warm scans the Account table and populates the ShardedCache.
// It respects context cancellation for graceful shutdown.
// Returns the number of accounts warmed.
func (w *Warmer) Warm(ctx context.Context) error {
	if w.cache == nil || w.db == nil {
		return nil
	}

	tx, err := w.db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var count int
	err = tx.ForEach(modules.Account, nil, func(k, v []byte) error {
		if count >= w.maxAccounts {
			return errStopIteration
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Copy key and value to avoid holding references to mmap'd memory.
		key := make([]byte, len(k))
		copy(key, k)

		w.cache.Put(modules.Account, key, v)
		count++
		return nil
	})

	if err == errStopIteration {
		err = nil
	}

	snapshotCacheWarmedAccounts.Set(uint64(count))
	log.Info("Snapshot cache warmup completed", "accounts", count)
	return err
}

// errStopIteration is a sentinel error used to break ForEach iteration.
var errStopIteration = errors.New("stop iteration")
