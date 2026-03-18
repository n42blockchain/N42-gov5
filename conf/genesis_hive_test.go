package conf

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestApplyHiveGenesisEnvCreatesFakerConfig(t *testing.T) {
	t.Parallel()

	genesis := &Genesis{}
	env := map[string]string{
		"HIVE_CHAIN_ID":                  "7",
		"HIVE_FORK_HOMESTEAD":            "0",
		"HIVE_FORK_BERLIN":               "0",
		"HIVE_FORK_LONDON":               "0",
		"HIVE_TERMINAL_TOTAL_DIFFICULTY": "0",
		"HIVE_SHANGHAI_TIMESTAMP":        "0",
		"HIVE_PRAGUE_TIMESTAMP":          "15000",
	}

	applied := ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	if !applied {
		t.Fatal("ApplyHiveGenesisEnv should report applied overrides")
	}
	if genesis.Config == nil {
		t.Fatal("genesis.Config should not be nil")
	}
	if genesis.Config.Consensus != params.Faker {
		t.Fatalf("unexpected consensus: got %q want %q", genesis.Config.Consensus, params.Faker)
	}
	if genesis.Config.ChainID == nil || genesis.Config.ChainID.Uint64() != 7 {
		t.Fatalf("unexpected chain id: %v", genesis.Config.ChainID)
	}
	if genesis.Config.HomesteadBlock == nil || genesis.Config.HomesteadBlock.Uint64() != 0 {
		t.Fatalf("unexpected homestead block: %v", genesis.Config.HomesteadBlock)
	}
	if genesis.Config.LondonBlock == nil || genesis.Config.LondonBlock.Uint64() != 0 {
		t.Fatalf("unexpected london block: %v", genesis.Config.LondonBlock)
	}
	if genesis.Config.TerminalTotalDifficulty == nil || genesis.Config.TerminalTotalDifficulty.Uint64() != 0 {
		t.Fatalf("unexpected terminal total difficulty: %v", genesis.Config.TerminalTotalDifficulty)
	}
	if !genesis.Config.TerminalTotalDifficultyPassed {
		t.Fatal("terminal total difficulty passed should be true when Hive TTD is set")
	}
	if genesis.Config.ShanghaiBlock == nil || genesis.Config.ShanghaiBlock.Uint64() != 0 {
		t.Fatalf("unexpected shanghai activation: %v", genesis.Config.ShanghaiBlock)
	}
	if genesis.Config.PragueTime == nil || genesis.Config.PragueTime.Uint64() != 15000 {
		t.Fatalf("unexpected prague time: %v", genesis.Config.PragueTime)
	}
}

func TestApplyHiveGenesisEnvSupportsBPOForkTimestamps(t *testing.T) {
	t.Parallel()

	genesis := &Genesis{
		Config: &params.ChainConfig{
			OsakaTime: big.NewInt(0),
		},
	}
	env := map[string]string{
		"HIVE_OSAKA_TIMESTAMP": "0",
		"HIVE_BPO1_TIMESTAMP":  "15000",
	}

	ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})

	if genesis.Config == nil {
		t.Fatal("genesis.Config should not be nil")
	}
	if genesis.Config.BPO1Time == nil || genesis.Config.BPO1Time.Uint64() != 15000 {
		t.Fatalf("unexpected BPO1 time: %v", genesis.Config.BPO1Time)
	}
	if got := genesis.Config.BlobMaxBlobsPerBlock(0); got != 9 {
		t.Fatalf("BlobMaxBlobsPerBlock(0) = %d, want 9", got)
	}
	if got := genesis.Config.BlobMaxBlobsPerBlock(15000); got != 15 {
		t.Fatalf("BlobMaxBlobsPerBlock(15000) = %d, want 15", got)
	}
}

func TestApplyHiveGenesisEnvSupportsClique(t *testing.T) {
	t.Parallel()

	genesis := &Genesis{}
	env := map[string]string{
		"HIVE_CHAIN_ID":      "17",
		"HIVE_CLIQUE_PERIOD": "5",
	}

	ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})

	if genesis.Config == nil {
		t.Fatal("genesis.Config should not be nil")
	}
	if genesis.Config.Consensus != params.CliqueConsensus {
		t.Fatalf("unexpected consensus: got %q want %q", genesis.Config.Consensus, params.CliqueConsensus)
	}
	if genesis.Config.Clique == nil {
		t.Fatal("clique config should not be nil")
	}
	if genesis.Config.Clique.Period != 5 {
		t.Fatalf("unexpected clique period: got %d want 5", genesis.Config.Clique.Period)
	}
	if genesis.Config.Clique.Epoch != 30000 {
		t.Fatalf("unexpected clique epoch: got %d want 30000", genesis.Config.Clique.Epoch)
	}
}
