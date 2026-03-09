package snapshot

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type mockBlockProvider struct {
	block uint64
}

func (m *mockBlockProvider) CurrentBlock() uint64 { return m.block }

func TestManager_CreateAndGC(t *testing.T) {
	db := testDB(t)
	bp := &mockBlockProvider{block: 0}

	// Seed a few accounts so count > 0.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 5; i++ {
			addr := make([]byte, 20)
			addr[0] = byte(i)
			if err := tx.Put(modules.Account, addr, []byte{0x01}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := defaultTestConfig()
	cfg.Interval = 100
	cfg.MaxRetained = 2

	mgr := NewManager(cfg, db, bp)

	// Block 0 → nothing should happen.
	mgr.maybeCreate(cfg.Interval)
	assertSnapshotCount(t, db, 0)

	// Block 200 → snapshot at 100 (aligned to interval, minus safety margin).
	bp.block = 200
	mgr.maybeCreate(cfg.Interval)
	assertSnapshotCount(t, db, 1)
	assertSnapshotExists(t, db, 100)

	// Block 350 → snapshot at 200.
	bp.block = 350
	mgr.maybeCreate(cfg.Interval)
	assertSnapshotCount(t, db, 2)
	assertSnapshotExists(t, db, 200)

	// Block 450 → snapshot at 300, GC should remove 100.
	bp.block = 450
	mgr.maybeCreate(cfg.Interval)
	if err := mgr.gc(); err != nil {
		t.Fatal(err)
	}
	assertSnapshotCount(t, db, 2)
	assertSnapshotNotExists(t, db, 100)
	assertSnapshotExists(t, db, 200)
	assertSnapshotExists(t, db, 300)
}

func TestManager_OldestRetainedBlock(t *testing.T) {
	db := testDB(t)
	bp := &mockBlockProvider{block: 500}

	cfg := defaultTestConfig()
	cfg.Interval = 100
	cfg.MaxRetained = 3

	mgr := NewManager(cfg, db, bp)

	// No snapshots → 0.
	if got := mgr.OldestRetainedBlock(); got != 0 {
		t.Errorf("empty: want 0, got %d", got)
	}

	// Create one snapshot.
	bp.block = 200
	mgr.maybeCreate(cfg.Interval)
	if got := mgr.OldestRetainedBlock(); got != 100 {
		t.Errorf("one snapshot: want 100, got %d", got)
	}
}

func TestManager_SkipDuplicateHeight(t *testing.T) {
	db := testDB(t)
	bp := &mockBlockProvider{block: 200}

	cfg := defaultTestConfig()
	cfg.Interval = 100

	mgr := NewManager(cfg, db, bp)
	mgr.maybeCreate(cfg.Interval)
	assertSnapshotCount(t, db, 1)

	// Same block → no new snapshot.
	mgr.maybeCreate(cfg.Interval)
	assertSnapshotCount(t, db, 1)
}

func defaultTestConfig() conf.SnapshotConfig {
	return conf.SnapshotConfig{
		Enable:       true,
		Interval:     100,
		MaxRetained:  3,
		SafetyMargin: 64,
	}
}

// --- assertion helpers ---

func assertSnapshotCount(t *testing.T, db kv.RwDB, want int) {
	t.Helper()
	var got int
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		list, err := ListSnapshots(tx)
		if err != nil {
			return err
		}
		got = len(list)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("snapshot count: want %d, got %d", want, got)
	}
}

func assertSnapshotExists(t *testing.T, db kv.RwDB, height uint64) {
	t.Helper()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		m, err := LoadSnapshot(tx, height)
		if err != nil {
			return err
		}
		if m == nil {
			t.Errorf("expected snapshot at %d, got nil", height)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotNotExists(t *testing.T, db kv.RwDB, height uint64) {
	t.Helper()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		m, err := LoadSnapshot(tx, height)
		if err != nil {
			return err
		}
		if m != nil {
			t.Errorf("expected no snapshot at %d, got %+v", height, m)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
