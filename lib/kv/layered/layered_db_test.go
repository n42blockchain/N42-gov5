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
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func newTestLayeredDB(t *testing.T) *LayeredDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	stateDB := memdb.New(t.TempDir())
	historyDB := memdb.New(t.TempDir())
	t.Cleanup(func() {
		stateDB.Close()
		historyDB.Close()
	})

	cfg := &Config{
		Enable:        true,
		CacheShards:   16,
		CacheCapacity: 1000,
		MaxMemoryMB:   64,
	}
	return NewLayeredDB(stateDB, historyDB, cfg)
}

func TestLayeredDB_HotTableReadWrite(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// Write to Account (hot table).
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("addr1"), []byte("balance100"))
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read back.
	err = db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("addr1"))
		if err != nil {
			return err
		}
		if string(v) != "balance100" {
			t.Errorf("expected balance100, got %s", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's cached.
	hits, _, _ := db.Cache().Stats()
	if hits == 0 {
		t.Error("expected cache hit after read-back")
	}
}

func TestLayeredDB_ColdTableReadWrite(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// Write to AccountChangeSet (cold table).
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("AccountChangeSet", []byte("block1"), []byte("changeset"))
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read back — should not be cached.
	err = db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("AccountChangeSet", []byte("block1"))
		if err != nil {
			return err
		}
		if string(v) != "changeset" {
			t.Errorf("expected changeset, got %s", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Cold tables are NOT cached.
	_, _, entries := db.Cache().Stats()
	if entries != 0 {
		t.Errorf("expected no cache entries for cold table, got %d", entries)
	}
}

func TestLayeredDB_CrossTableTransaction(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// A single transaction writes to both hot and cold tables.
	err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put("Account", []byte("addr"), []byte("val")); err != nil {
			return err
		}
		return tx.Put("AccountChangeSet", []byte("block"), []byte("cs"))
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify both are readable.
	err = db.View(ctx, func(tx kv.Tx) error {
		v1, err := tx.GetOne("Account", []byte("addr"))
		if err != nil {
			return err
		}
		if string(v1) != "val" {
			t.Errorf("Account: expected val, got %s", v1)
		}

		v2, err := tx.GetOne("AccountChangeSet", []byte("block"))
		if err != nil {
			return err
		}
		if string(v2) != "cs" {
			t.Errorf("AccountChangeSet: expected cs, got %s", v2)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredDB_CacheInvalidationOnDelete(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// Put and read (populates cache).
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("addr"), []byte("val"))
	})
	_ = db.View(ctx, func(tx kv.Tx) error {
		_, _ = tx.GetOne("Account", []byte("addr"))
		return nil
	})

	// Delete.
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Delete("Account", []byte("addr"))
	})

	// Should not be in cache after delete.
	_, ok := db.Cache().Get("Account", []byte("addr"))
	if ok {
		t.Error("expected cache miss after delete")
	}
}

func TestLayeredDB_CacheUpdateOnPut(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// Put initial value.
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("addr"), []byte("old"))
	})

	// Update value.
	_ = db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("addr"), []byte("new"))
	})

	// Cache should have the new value.
	v, ok := db.Cache().Get("Account", []byte("addr"))
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(v) != "new" {
		t.Errorf("expected 'new', got %s", v)
	}
}

func TestLayeredDB_RollbackDoesNotPersist(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put("Account", []byte("addr"), []byte("val")); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()

	// Should not be readable after rollback.
	err = db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("addr"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Errorf("expected nil after rollback, got %s", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredDB_DBSize(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	err := db.View(ctx, func(tx kv.Tx) error {
		size, err := tx.DBSize()
		if err != nil {
			return err
		}
		// Should be > 0 (at least page headers).
		if size == 0 {
			t.Error("expected non-zero DB size")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredDB_TableRouting(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()

	// Verify hot tables route to stateDB.
	hotNames := []string{"Account", "Storage", "Code", "PlainCodeHash", "IncarnationMap"}
	for _, name := range hotNames {
		if IsColdTable(name) {
			t.Errorf("%s should not be cold", name)
		}
		if !IsHotTable(name) {
			t.Errorf("%s should be hot", name)
		}
	}

	// Verify cold tables route to historyDB.
	coldNames := []string{"AccountChangeSet", "StorageChangeSet", "AccountHistory",
		"StorageHistory", "Receipt", "TransactionLog"}
	for _, name := range coldNames {
		if !IsColdTable(name) {
			t.Errorf("%s should be cold", name)
		}
		if IsHotTable(name) {
			t.Errorf("%s should not be hot", name)
		}
	}
}

func TestLayeredDB_UnclassifiedGoesToState(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	// "Sequence" is not explicitly hot or cold — should go to stateDB.
	err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Sequence", []byte("test"), []byte("42"))
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's readable from stateDB.
	err = db.StateDB().View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Sequence", []byte("test"))
		if err != nil {
			return err
		}
		if string(v) != "42" {
			t.Errorf("expected 42, got %s", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"disabled", Config{Enable: false}, false},
		{"defaults", Config{Enable: true}, false},
		{"shards_not_power_of_2", Config{Enable: true, CacheShards: 3}, true},
		{"shards_too_large", Config{Enable: true, CacheShards: 2048}, true},
		{"capacity_too_large", Config{Enable: true, CacheCapacity: 20_000_000}, true},
		{"memory_too_large", Config{Enable: true, MaxMemoryMB: 100_000}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
