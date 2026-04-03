package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func TestStateRootBlock1(t *testing.T) {
	skipIfNoGeth(t)

	f, err := freezer.New(gethAncientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	chainCfg := params.EthereumMainnetChainConfig
	gPath := genesisPath()

	// Load genesis state.
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count, err := InitEthGenesisState(tx, gPath)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Genesis: %d accounts", count)

	engine := NewEthReplayEngine(chainCfg)

	// Execute blocks 1-10 with state root verification every block.
	cfg := ExecutorConfig{
		StartBlock:     1,
		EndBlock:       10,
		CommitInterval: 100,
		VerifyInterval: 1, // verify every block
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg)
	err = executor.Run(context.Background())
	if err != nil {
		t.Fatalf("execution with state root verification failed: %v", err)
	}
	t.Log("State root verified for blocks 1-10!")
}

func TestStateRootBlock50K(t *testing.T) {
	skipIfNoGeth(t)

	f, err := freezer.New(gethAncientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	chainCfg := params.EthereumMainnetChainConfig
	gPath := genesisPath()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitEthGenesisState(tx, gPath); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	engine := NewEthReplayEngine(chainCfg)

	// 100K blocks with state root verification every 10000 blocks.
	// This covers blocks up to and past the first ETH transaction (46147).
	cfg := ExecutorConfig{
		StartBlock:     1,
		EndBlock:       100000,
		CommitInterval: 100000,
		VerifyInterval: 10000,
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg)
	err = executor.Run(context.Background())
	if err != nil {
		t.Fatalf("executor with state root verification failed: %v", err)
	}
	t.Log("50K blocks with state root verification every 5000 blocks: ALL PASS!")
}

func TestStateRootBlock1000(t *testing.T) {
	skipIfNoGeth(t)

	f, err := freezer.New(gethAncientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	chainCfg := params.EthereumMainnetChainConfig
	gPath := genesisPath()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitEthGenesisState(tx, gPath); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	engine := NewEthReplayEngine(chainCfg)

	// Execute 1000 blocks with verification every 100 blocks.
	cfg := ExecutorConfig{
		StartBlock:     1,
		EndBlock:       1000,
		CommitInterval: 1000,
		VerifyInterval: 100, // verify every 100 blocks
	}

	executor := NewExecutor(f, db, chainCfg, engine, cfg)
	err = executor.Run(context.Background())
	if err != nil {
		t.Fatalf("execution with state root verification failed: %v", err)
	}
	t.Log("State root verified at blocks 100, 200, ..., 1000!")
}
