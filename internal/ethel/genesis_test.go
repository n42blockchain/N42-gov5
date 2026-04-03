package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestInitEthGenesisState(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	gPath := genesisPath()
	count, err := InitEthGenesisState(tx, gPath)
	if err != nil {
		tx.Rollback()
		t.Fatalf("init genesis: %v", err)
	}
	if count != 8893 {
		t.Errorf("accounts: got %d, want 8893", count)
	}

	// Second call should skip (already initialized).
	count2, err := InitEthGenesisState(tx, gPath)
	if err != nil {
		t.Fatal(err)
	}
	if count2 != 0 {
		t.Errorf("second init: got %d, want 0 (skip)", count2)
	}

	tx.Rollback()
}

func TestIsGenesisInitialized(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if IsGenesisInitialized(tx) {
		t.Error("empty DB should not be initialized")
	}

	InitEthGenesisState(tx, genesisPath())

	if !IsGenesisInitialized(tx) {
		t.Error("after init, should be initialized")
	}

	tx.Rollback()
}
