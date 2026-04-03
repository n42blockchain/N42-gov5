package ethel

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func genesisPath() string {
	// Navigate from test file to project root.
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "params", "chainspecs", "eth_mainnet_genesis.json")
}

func TestExecutorWithGenesis100K(t *testing.T) {
	skipIfNoGeth(t)

	f, err := freezer.New(gethAncientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	chainCfg := params.EthereumMainnetChainConfig

	// Load Ethereum mainnet genesis state.
	gPath := genesisPath()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count, err := InitEthGenesisState(tx, gPath)
	if err != nil {
		tx.Rollback()
		t.Fatalf("init genesis: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Genesis loaded: %d accounts", count)

	engine := NewEthReplayEngine(chainCfg)

	// Execute blocks 1-100K (block 0 = genesis, already loaded).
	// Includes first transaction at 46147.
	cfg := ExecutorConfig{
		StartBlock:     1,
		EndBlock:       99_999,
		CommitInterval: 10000,
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg, nil)
	if err := executor.Run(context.Background()); err != nil {
		t.Fatalf("executor failed: %v", err)
	}
	t.Log("Successfully executed 100K blocks including first Ethereum transactions!")
}
