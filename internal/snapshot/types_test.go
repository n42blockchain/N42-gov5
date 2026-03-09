package snapshot

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func testDB(t *testing.T) kv.RwDB {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return memdb.NewTestDB(t)
}

func TestSaveAndLoadSnapshot(t *testing.T) {
	db := testDB(t)

	m := &Meta{
		BlockNumber:  10000,
		CreatedAt:    1700000000,
		AccountCount: 500,
		StorageCount: 2000,
		CodeCount:    10,
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveSnapshot(tx, m)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded *Meta
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadSnapshot(tx, 10000)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if loaded == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if loaded.BlockNumber != 10000 {
		t.Errorf("block number: want 10000, got %d", loaded.BlockNumber)
	}
	if loaded.AccountCount != 500 {
		t.Errorf("account count: want 500, got %d", loaded.AccountCount)
	}
}

func TestLoadSnapshotNotFound(t *testing.T) {
	db := testDB(t)

	var loaded *Meta
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadSnapshot(tx, 99999)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Errorf("expected nil for non-existent snapshot, got %+v", loaded)
	}
}

func TestListSnapshots(t *testing.T) {
	db := testDB(t)

	heights := []uint64{10000, 20000, 30000}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, h := range heights {
			if err := SaveSnapshot(tx, &Meta{BlockNumber: h, CreatedAt: int64(h)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var list []*Meta
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		list, err = ListSnapshots(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if len(list) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(list))
	}
	// Should be in ascending order.
	for i, m := range list {
		if m.BlockNumber != heights[i] {
			t.Errorf("snapshot[%d]: want block %d, got %d", i, heights[i], m.BlockNumber)
		}
	}
}

func TestLatestAndOldestSnapshot(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, h := range []uint64{5000, 15000, 25000} {
			if err := SaveSnapshot(tx, &Meta{BlockNumber: h}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		latest, err := LatestSnapshot(tx)
		if err != nil {
			return err
		}
		if latest == nil || latest.BlockNumber != 25000 {
			t.Errorf("latest: want 25000, got %+v", latest)
		}

		oldest, err := OldestSnapshot(tx)
		if err != nil {
			return err
		}
		if oldest == nil || oldest.BlockNumber != 5000 {
			t.Errorf("oldest: want 5000, got %+v", oldest)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := SaveSnapshot(tx, &Meta{BlockNumber: 10000}); err != nil {
			return err
		}
		return SaveSnapshot(tx, &Meta{BlockNumber: 20000})
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return DeleteSnapshot(tx, 10000)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		list, err := ListSnapshots(tx)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			t.Errorf("expected 1 snapshot after delete, got %d", len(list))
		}
		if list[0].BlockNumber != 20000 {
			t.Errorf("remaining snapshot: want 20000, got %d", list[0].BlockNumber)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
