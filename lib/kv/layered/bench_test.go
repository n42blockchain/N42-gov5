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
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func BenchmarkCacheGet(b *testing.B) {
	c := NewShardedCache(256, 100000)
	// Pre-populate 10K entries.
	for i := 0; i < 10000; i++ {
		key := []byte(fmt.Sprintf("addr-%06d", i))
		val := []byte(fmt.Sprintf("balance-%d", i))
		c.Put("Account", key, val)
	}

	key := []byte("addr-005000")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get("Account", key)
	}
}

func BenchmarkCachePut(b *testing.B) {
	c := NewShardedCache(256, 100000)
	key := []byte("addr-benchmark")
	val := []byte("balance-999")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Put("Account", key, val)
	}
}

func BenchmarkLayeredDB_GetOne_CacheHit(b *testing.B) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	stateDB := memdb.New(b.TempDir())
	historyDB := memdb.New(b.TempDir())
	defer stateDB.Close()
	defer historyDB.Close()

	cfg := &Config{Enable: true, CacheShards: 256, CacheCapacity: 100000}
	db := NewLayeredDB(stateDB, historyDB, cfg)
	defer db.Close()

	ctx := context.Background()

	// Write 1000 accounts.
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		for i := 0; i < 1000; i++ {
			key := []byte(fmt.Sprintf("addr-%06d", i))
			val := []byte(fmt.Sprintf("balance-%d", i))
			_ = tx.Put("Account", key, val)
		}
		return nil
	})

	// Pre-warm cache via read.
	_ = db.View(ctx, func(tx kv.Tx) error {
		for i := 0; i < 1000; i++ {
			key := []byte(fmt.Sprintf("addr-%06d", i))
			_, _ = tx.GetOne("Account", key)
		}
		return nil
	})

	key := []byte("addr-000500")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = db.View(ctx, func(tx kv.Tx) error {
			_, _ = tx.GetOne("Account", key)
			return nil
		})
	}
}

func BenchmarkLayeredDB_GetOne_CacheMiss(b *testing.B) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	stateDB := memdb.New(b.TempDir())
	historyDB := memdb.New(b.TempDir())
	defer stateDB.Close()
	defer historyDB.Close()

	cfg := &Config{Enable: true, CacheShards: 256, CacheCapacity: 100000}
	db := NewLayeredDB(stateDB, historyDB, cfg)
	defer db.Close()

	ctx := context.Background()

	// Write 1000 accounts but don't pre-warm cache.
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		for i := 0; i < 1000; i++ {
			key := []byte(fmt.Sprintf("addr-%06d", i))
			val := []byte(fmt.Sprintf("balance-%d", i))
			_ = tx.Put("Account", key, val)
		}
		return nil
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("addr-%06d", i%1000))
		_ = db.View(ctx, func(tx kv.Tx) error {
			_, _ = tx.GetOne("Account", key)
			return nil
		})
	}
}
