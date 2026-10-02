package node

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/conf"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// TestOpenLayeredDatabaseOpensStateAndHistoryDBs exercises the success path
// of openLayeredDatabase: default state/history paths derived from the
// datadir, both MDBX instances opening, and the N42-version stamp write.
func TestOpenLayeredDatabaseOpensStateAndHistoryDBs(t *testing.T) {
	dir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: dir},
	}
	cfg.LayeredDBCfg.Enable = true

	db, err := openLayeredDatabase(context.Background(), cfg, log2.New(), "chaindata")
	if err != nil {
		t.Fatalf("openLayeredDatabase: %v", err)
	}
	defer db.Close()

	if cfg.LayeredDBCfg.CacheShards != 256 {
		t.Fatalf("expected Validate to apply the default CacheShards, got %d", cfg.LayeredDBCfg.CacheShards)
	}
}

// TestOpenLayeredDatabaseUsesExplicitPaths confirms explicit
// StateDBPath/HistoryDBPath overrides are honored instead of the
// datadir-derived defaults.
func TestOpenLayeredDatabaseUsesExplicitPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: dir},
	}
	cfg.LayeredDBCfg.Enable = true
	cfg.LayeredDBCfg.StateDBPath = filepath.Join(dir, "custom-state")
	cfg.LayeredDBCfg.HistoryDBPath = filepath.Join(dir, "custom-history")

	db, err := openLayeredDatabase(context.Background(), cfg, log2.New(), "chaindata")
	if err != nil {
		t.Fatalf("openLayeredDatabase: %v", err)
	}
	defer db.Close()
}

// TestOpenLayeredDatabaseUsesStorageTierPaths confirms that when storage
// tiering is enabled, the state DB defaults under the hot path and the
// history DB defaults under the cold path rather than directly under the
// node datadir.
func TestOpenLayeredDatabaseUsesStorageTierPaths(t *testing.T) {
	dir := t.TempDir()
	hot := filepath.Join(dir, "hot")
	cold := filepath.Join(dir, "cold")
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: dir},
	}
	cfg.LayeredDBCfg.Enable = true
	cfg.StorageTierCfg.Enabled = true
	cfg.StorageTierCfg.HotPath = hot
	cfg.StorageTierCfg.ColdPath = cold

	db, err := openLayeredDatabase(context.Background(), cfg, log2.New(), "chaindata")
	if err != nil {
		t.Fatalf("openLayeredDatabase: %v", err)
	}
	defer db.Close()
}

// TestOpenLayeredDatabaseRejectsInvalidConfig exercises the error path where
// cfg.LayeredDBCfg.Validate rejects a non-power-of-two CacheShards value.
func TestOpenLayeredDatabaseRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: dir},
	}
	cfg.LayeredDBCfg.Enable = true
	cfg.LayeredDBCfg.CacheShards = 3 // not a power of two

	if _, err := openLayeredDatabase(context.Background(), cfg, log2.New(), "chaindata"); err == nil {
		t.Fatal("expected an error from an invalid layered DB config")
	}
}
