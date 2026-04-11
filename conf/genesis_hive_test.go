package conf

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
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
		"HIVE_CANCUN_TIMESTAMP":          "15000",
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
	if genesis.Config.ShanghaiTime == nil || genesis.Config.ShanghaiTime.Uint64() != 0 {
		t.Fatalf("unexpected shanghai time: %v", genesis.Config.ShanghaiTime)
	}
	if genesis.Config.CancunTime == nil || genesis.Config.CancunTime.Uint64() != 15000 {
		t.Fatalf("unexpected cancun time: %v", genesis.Config.CancunTime)
	}
	if genesis.Config.ShanghaiBlock != nil {
		t.Fatalf("unexpected shanghai block override: %v", genesis.Config.ShanghaiBlock)
	}
	if genesis.Config.CancunBlock != nil {
		t.Fatalf("unexpected cancun block override: %v", genesis.Config.CancunBlock)
	}
	if genesis.Config.PragueTime == nil || genesis.Config.PragueTime.Uint64() != 15000 {
		t.Fatalf("unexpected prague time: %v", genesis.Config.PragueTime)
	}
}

func TestApplyHiveGenesisEnvFallsBackToHiveNetworkIDForChainID(t *testing.T) {
	t.Parallel()

	genesis := &Genesis{}
	env := map[string]string{
		"HIVE_NETWORK_ID": "1",
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
	if genesis.Config.ChainID == nil || genesis.Config.ChainID.Uint64() != 1 {
		t.Fatalf("unexpected chain id: %v", genesis.Config.ChainID)
	}
}

func TestApplyHiveGenesisEnvDefaultsAncestorForksForTimestampForks(t *testing.T) {
	t.Parallel()

	genesis := &Genesis{}
	env := map[string]string{
		"HIVE_NETWORK_ID":         "1",
		"HIVE_SHANGHAI_TIMESTAMP": "0",
		"HIVE_CANCUN_TIMESTAMP":   "0",
		"HIVE_PRAGUE_TIMESTAMP":   "0",
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
	for name, got := range map[string]*big.Int{
		"HomesteadBlock":        genesis.Config.HomesteadBlock,
		"TangerineWhistleBlock": genesis.Config.TangerineWhistleBlock,
		"SpuriousDragonBlock":   genesis.Config.SpuriousDragonBlock,
		"ByzantiumBlock":        genesis.Config.ByzantiumBlock,
		"ConstantinopleBlock":   genesis.Config.ConstantinopleBlock,
		"PetersburgBlock":       genesis.Config.PetersburgBlock,
		"IstanbulBlock":         genesis.Config.IstanbulBlock,
		"MuirGlacierBlock":      genesis.Config.MuirGlacierBlock,
		"BerlinBlock":           genesis.Config.BerlinBlock,
		"LondonBlock":           genesis.Config.LondonBlock,
	} {
		if got == nil || got.Sign() != 0 {
			t.Fatalf("%s = %v, want 0", name, got)
		}
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

func TestCloneChainConfigPreservesExtendedFields(t *testing.T) {
	t.Parallel()

	feeCollector := types.HexToAddress("0x00000000000000000000000000000000000000f1")
	src := &params.ChainConfig{
		GlamsterdamTime:               big.NewInt(1),
		PQPrecompilesTime:             big.NewInt(2),
		ContentStoreTime:              big.NewInt(3),
		AIInferenceTime:               big.NewInt(4),
		RandomnessTime:                big.NewInt(5),
		LtHashTime:                    big.NewInt(6),
		Eip1559FeeCollector:           &feeCollector,
		Eip1559FeeCollectorTransition: big.NewInt(7),
		Aura:                          &params.AuRaConfig{},
		Parlia:                        &params.ParliaConfig{},
		Bor:                           &params.BorConfig{},
	}

	cloned := cloneChainConfig(src)
	if cloned == nil {
		t.Fatal("cloneChainConfig() returned nil")
	}
	if cloned.GlamsterdamTime == nil || cloned.GlamsterdamTime.Uint64() != 1 {
		t.Fatalf("unexpected glamsterdam time: %v", cloned.GlamsterdamTime)
	}
	if cloned.PQPrecompilesTime == nil || cloned.PQPrecompilesTime.Uint64() != 2 {
		t.Fatalf("unexpected pq precompile time: %v", cloned.PQPrecompilesTime)
	}
	if cloned.ContentStoreTime == nil || cloned.ContentStoreTime.Uint64() != 3 {
		t.Fatalf("unexpected content store time: %v", cloned.ContentStoreTime)
	}
	if cloned.AIInferenceTime == nil || cloned.AIInferenceTime.Uint64() != 4 {
		t.Fatalf("unexpected ai inference time: %v", cloned.AIInferenceTime)
	}
	if cloned.RandomnessTime == nil || cloned.RandomnessTime.Uint64() != 5 {
		t.Fatalf("unexpected randomness time: %v", cloned.RandomnessTime)
	}
	if cloned.LtHashTime == nil || cloned.LtHashTime.Uint64() != 6 {
		t.Fatalf("unexpected lthash time: %v", cloned.LtHashTime)
	}
	if cloned.Eip1559FeeCollector == nil || *cloned.Eip1559FeeCollector != feeCollector {
		t.Fatalf("unexpected fee collector: %v", cloned.Eip1559FeeCollector)
	}
	if cloned.Aura == nil || cloned.Parlia == nil || cloned.Bor == nil {
		t.Fatal("expected consensus sub-config pointers to be preserved")
	}
	if cloned.GlamsterdamTime == src.GlamsterdamTime || cloned.Eip1559FeeCollector == src.Eip1559FeeCollector || cloned.Bor == src.Bor {
		t.Fatal("expected cloneChainConfig() to deep-copy pointer fields")
	}
}
