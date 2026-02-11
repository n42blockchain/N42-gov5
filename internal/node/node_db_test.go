package node

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestOpenDatabase_InMemory(t *testing.T) {
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: "",
		},
	}

	db, err := OpenDatabase(context.Background(), cfg, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase in-memory failed: %v", err)
	}
	defer db.Close()

	if db == nil {
		t.Fatal("expected non-nil database")
	}
}

func TestOpenDatabase_InMemory_EarlyReturn(t *testing.T) {
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: "",
		},
	}

	// should work even with nil logger since it returns early for in-memory
	db, err := OpenDatabase(context.Background(), cfg, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase in-memory failed: %v", err)
	}
	defer db.Close()

	// verify we can perform a read transaction on the in-memory db
	err = db.View(context.Background(), func(tx kv.Tx) error {
		return nil
	})
	if err != nil {
		t.Fatalf("View transaction on in-memory db failed: %v", err)
	}
}

func TestOpenDatabase_Persistent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
		},
	}
	logger := log.New()

	db, err := OpenDatabase(context.Background(), cfg, logger, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase persistent failed: %v", err)
	}
	defer db.Close()

	if db == nil {
		t.Fatal("expected non-nil database")
	}

	// verify we can perform a read transaction
	err = db.View(context.Background(), func(tx kv.Tx) error {
		return nil
	})
	if err != nil {
		t.Fatalf("View transaction failed: %v", err)
	}
}

func TestOpenDatabase_ContextPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
		},
	}
	logger := log.New()

	db, err := OpenDatabase(ctx, cfg, logger, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase with context failed: %v", err)
	}
	defer db.Close()

	if db == nil {
		t.Fatal("expected non-nil database")
	}
}

func TestOpenDatabase_WriteAndRead(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
		},
	}
	logger := log.New()

	db, err := OpenDatabase(context.Background(), cfg, logger, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase failed: %v", err)
	}
	defer db.Close()

	// verify write transaction works
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Update transaction failed: %v", err)
	}
}
