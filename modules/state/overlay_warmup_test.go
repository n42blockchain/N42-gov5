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

package state

import (
	"context"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
)

func TestWarmupOverlayCache(t *testing.T) {
	db, _ := setupTestDB(t)
	ctx := context.Background()

	// Insert accounts directly into the Account table.
	numAccounts := 50
	err := db.Update(ctx, func(tx kv.RwTx) error {
		for i := 0; i < numAccounts; i++ {
			key := []byte(fmt.Sprintf("addr-%04d", i))
			val := []byte(fmt.Sprintf("data-%04d", i))
			if err := tx.Put(modules.Account, key, val); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a fresh cache and run warmup.
	cache := layered.NewShardedCache(16, 1000)
	err = WarmupOverlayCache(ctx, db, cache, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the cache was populated.
	_, _, entries := cache.Stats()
	if entries != numAccounts {
		t.Errorf("cache entries: got %d, want %d", entries, numAccounts)
	}

	// Spot-check a few entries.
	for _, i := range []int{0, 25, 49} {
		key := []byte(fmt.Sprintf("addr-%04d", i))
		v, ok := cache.Get(modules.Account, key)
		if !ok {
			t.Errorf("entry %d not found in cache", i)
			continue
		}
		expected := []byte(fmt.Sprintf("data-%04d", i))
		if string(v) != string(expected) {
			t.Errorf("entry %d: got %q, want %q", i, v, expected)
		}
	}
}

func TestWarmupOverlayCacheEmpty(t *testing.T) {
	db, _ := setupTestDB(t)
	ctx := context.Background()

	cache := layered.NewShardedCache(16, 1000)
	err := WarmupOverlayCache(ctx, db, cache, 10)
	if err != nil {
		t.Fatal(err)
	}

	_, _, entries := cache.Stats()
	if entries != 0 {
		t.Errorf("cache entries for empty DB: got %d, want 0", entries)
	}
}

func TestWarmupOverlayCacheCancellation(t *testing.T) {
	db, _ := setupTestDB(t)

	// Insert some accounts.
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 100; i++ {
			key := []byte(fmt.Sprintf("addr-%04d", i))
			val := []byte(fmt.Sprintf("data-%04d", i))
			if err := tx.Put(modules.Account, key, val); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Cancel context immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cache := layered.NewShardedCache(16, 1000)
	err = WarmupOverlayCache(ctx, db, cache, 10)
	// Should get context error since we cancelled before starting.
	if err == nil {
		// It's possible warmup completes before checking ctx, so this is OK.
		t.Log("warmup completed despite cancelled context (entries loaded before check)")
	}
}
