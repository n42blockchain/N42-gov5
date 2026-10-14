package conf

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestNormalizeNetworkSelectionRejectsPerformanceBeforeReplacingConfig(t *testing.T) {
	for _, selection := range []struct{ chain, profile string }{
		{"mainnet", "n42"}, {"eth-mainnet", "eth"}, {"private", "eth"},
	} {
		t.Run(selection.chain+selection.profile, func(t *testing.T) {
			performance := &params.ChainConfig{
				ChainID:     big.NewInt(params.PerformanceChainID),
				Performance: &params.PerformanceChainConfig{},
			}
			cfg := &Config{NodeCfg: NodeConfig{Chain: selection.chain, Profile: selection.profile}, ChainCfg: performance}
			if err := NormalizeNetworkSelection(cfg); err == nil {
				t.Fatal("performance configuration silently replaced by preset")
			}
			if cfg.ChainCfg != performance {
				t.Fatal("rejected performance configuration was mutated")
			}
		})
	}
}

func TestNormalizeNetworkSelectionPreservesNilPerformance(t *testing.T) {
	for _, selection := range []struct{ chain, profile string }{
		{"mainnet", "n42"}, {"eth-mainnet", "eth"}, {"private", "n42"}, {"private", "eth"},
	} {
		cfg := &Config{NodeCfg: NodeConfig{Chain: selection.chain, Profile: selection.profile}}
		if err := NormalizeNetworkSelection(cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ChainCfg != nil && cfg.ChainCfg.Performance != nil {
			t.Fatal("preset silently enabled performance mode")
		}
	}
}
