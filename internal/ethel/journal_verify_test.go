package ethel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// TestJournalVerifySmall verifies journal replay + changeset revert on a
// small block range. Requires Geth ancient data at the standard path.
// Run with: go test -run TestJournalVerifySmall -timeout 5m
func TestJournalVerifySmall(t *testing.T) {
	ancientPath := gethAncientPath
	genesisPath := filepath.Join("..", "..", "params", "chainspecs", "eth_mainnet_genesis.json")

	if _, err := os.Stat(ancientPath); err != nil {
		t.Skipf("Geth ancient data not found: %s", ancientPath)
	}

	// 1. Generate journal for 10K blocks.
	tmpDir := t.TempDir()

	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inputF.Close()

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(tmpDir).
		Label(kv.ChainDB).
		PageSize(4096).
		WriteMap().
		DirtySpace(uint64(256 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Load genesis.
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count, err := InitEthGenesisState(tx, genesisPath)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Genesis loaded: %d accounts", count)

	// Execute blocks 0-10000.
	outAncient := filepath.Join(tmpDir, "ancient")
	outF, err := freezer.New(outAncient, 0)
	if err != nil {
		t.Fatal(err)
	}

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	cfg := ExecutorConfig{
		StartBlock:     0,
		EndBlock:       10000,
		CommitInterval: 5000,
	}
	executor := NewExecutor(inputF, db, chainCfg, engine, cfg, outF)

	if err := executor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	outF.Close()

	// 2. Verify journal + revert.
	verifyDir := t.TempDir()
	verifyDB, err := mdbx.NewMDBX(logger).
		Path(verifyDir).
		Label(kv.ChainDB).
		PageSize(4096).
		WriteMap().
		DirtySpace(uint64(256 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer verifyDB.Close()

	// Load genesis into verify DB.
	vtx, err := verifyDB.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitEthGenesisState(vtx, genesisPath); err != nil {
		vtx.Rollback()
		t.Fatal(err)
	}
	if err := vtx.Commit(); err != nil {
		t.Fatal(err)
	}

	outF2, err := freezer.New(outAncient, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer outF2.Close()

	verifier := NewJournalVerifier(verifyDB, inputF, outF2, 2000, 10001)
	if err := verifier.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Log("Journal verify + revert test passed")
}
