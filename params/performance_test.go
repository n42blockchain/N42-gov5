package params

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

func performanceTestConfig() *ChainConfig {
	return &ChainConfig{
		ChainID: big.NewInt(PerformanceChainID), Consensus: HotStuffConsensus,
		HotStuff: &HotStuffConfig{}, StateScheme: "qmdb",
		Performance: &PerformanceChainConfig{
			Version: 1, Mode: PerformanceModeIndependent, Role: PerformanceRoleLocal,
			Transactions: PerformanceTransactionsETH, NetworkNamespace: PerformanceNamespace,
		},
	}
}

func TestPerformanceNilPreservesDefaultsAndSerialization(t *testing.T) {
	for _, cfg := range []*ChainConfig{nil, MainnetChainConfig, MainnetQMDBStaggeredChainConfig, EthereumMainnetChainConfig} {
		profile, _ := ResolveExecutionProfile("eth")
		if err := cfg.ValidatePerformanceSelection("eth-mainnet", profile); err != nil {
			t.Fatal(err)
		}
		if cfg != nil {
			raw, err := json.Marshal(cfg)
			if err != nil || strings.Contains(string(raw), `"performance"`) {
				t.Fatalf("legacy serialization changed: %s, %v", raw, err)
			}
		}
	}
	// Chain ID alone never opts into new execution or transaction rules.
	cfg := &ChainConfig{ChainID: big.NewInt(PerformanceChainID)}
	profile, _ := ResolveExecutionProfile("n42")
	if err := cfg.ValidatePerformanceSelection("private", profile); err != nil || cfg.Performance != nil {
		t.Fatalf("chain ID silently activated performance mode: %v", err)
	}
}

func TestPerformanceSelectionRejectsUnavailableRolesAndNetworks(t *testing.T) {
	profile, _ := ResolveExecutionProfile("n42")
	if err := performanceTestConfig().ValidatePerformanceSelection("private", profile); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		change  func(*ChainConfig)
		chain   string
		profile string
	}{
		{"eth", func(c *ChainConfig) {}, "private", "eth"},
		{"native", func(c *ChainConfig) {}, "mainnet", "n42"},
		{"chain-id", func(c *ChainConfig) { c.ChainID = big.NewInt(94) }, "private", "n42"},
		{"large-chain-id", func(c *ChainConfig) { c.ChainID = new(big.Int).Lsh(big.NewInt(1), 100) }, "private", "n42"},
		{"shared", func(c *ChainConfig) { c.Performance.Mode = "shared-go-E1" }, "private", "n42"},
		{"validator", func(c *ChainConfig) { c.Performance.Role = "validator" }, "private", "n42"},
		{"empty-role", func(c *ChainConfig) { c.Performance.Role = "" }, "private", "n42"},
		{"ed25519", func(c *ChainConfig) { c.Performance.Transactions = "performance-ed25519" }, "private", "n42"},
		{"namespace", func(c *ChainConfig) { c.Performance.NetworkNamespace = "mainnet" }, "private", "n42"},
		{"version", func(c *ChainConfig) { c.Performance.Version = 2 }, "private", "n42"},
		{"consensus", func(c *ChainConfig) { c.Consensus = Faker }, "private", "n42"},
		{"state", func(c *ChainConfig) { c.StateScheme = "ethereum-mpt" }, "private", "n42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := performanceTestConfig()
			tc.change(cfg)
			profile, _ := ResolveExecutionProfile(tc.profile)
			if err := cfg.ValidatePerformanceSelection(tc.chain, profile); err == nil {
				t.Fatal("accepted unsupported performance configuration")
			}
		})
	}
}

func TestPerformanceConfigImmutableAtEveryHeight(t *testing.T) {
	stored := performanceTestConfig()
	for _, height := range []uint64{0, 1, 1000} {
		if err := stored.CheckCompatible(performanceTestConfig(), height); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*ChainConfig){
			func(c *ChainConfig) { c.Performance = nil },
			func(c *ChainConfig) { c.Performance.Role = "validator" },
			func(c *ChainConfig) { c.Performance.Mode = "shared-go-E1" },
			func(c *ChainConfig) { c.Performance.Transactions = "performance-ed25519" },
			func(c *ChainConfig) { c.Performance.NetworkNamespace = "other" },
			func(c *ChainConfig) { c.Performance.Version++ },
		} {
			requested := performanceTestConfig()
			mutate(requested)
			if err := stored.CheckCompatible(requested, height); err == nil || !strings.Contains(err.What, "performance") {
				t.Fatalf("performance configuration drift allowed at height %d: %v", height, err)
			}
		}
		legacy := &ChainConfig{}
		if err := legacy.CheckCompatible(stored, height); err == nil {
			t.Fatalf("enabled performance on existing genesis at height %d", height)
		}
	}
}
