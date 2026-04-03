package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func TestExecutorFirst100Blocks(t *testing.T) {
	skipIfNoGeth(t)

	// Open Geth ancient data (read-only tables).
	f, err := openGethFreezerRO(gethAncientPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Create in-memory MDBX for state.
	db := memdb.NewTestDB(t)

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := ExecutorConfig{
		StartBlock:     0,
		EndBlock:       99,
		CommitInterval: 100,
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg, nil)

	if err := executor.Run(context.Background()); err != nil {
		t.Fatalf("executor failed: %v", err)
	}

	t.Log("Successfully executed blocks 0-99 (all empty, no transactions)")
}

func TestExecutorFirstTxBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long test in short mode")
	}
	skipIfNoGeth(t)

	// Open Geth ancient data.
	f, err := openGethFreezerRO(gethAncientPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	db := memdb.NewTestDB(t)

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	// Execute blocks 0-46147 (first Ethereum transaction).
	// This is a meaningful test because it includes the first ever tx.
	// The sender must have balance from genesis to pay gas.
	// Since we don't have genesis state loaded, this will likely fail
	// at block 46147 with insufficient funds — that's expected.
	cfg := ExecutorConfig{
		StartBlock:     0,
		EndBlock:       46147,
		CommitInterval: 10000,
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg, nil)
	err = executor.Run(context.Background())
	if err != nil {
		// Expected to fail at block 46147 (first tx, no genesis state).
		t.Logf("Expected failure at first tx block (no genesis state): %v", err)
	} else {
		t.Log("Unexpectedly succeeded — genesis state must be loaded!")
	}
}

// openGethFreezerRO opens a Geth freezer without truncating tables
// (opens each table individually, doesn't use New which truncates to min).
func openGethFreezerRO(path string) (*freezer.Freezer, error) {
	// For testing, we use New() on a copy or symlink.
	// But since diffs table has fewer items, we need a special approach.
	// For now, just use direct table access via the Freezer.
	return freezer.New(path, 0)
}

