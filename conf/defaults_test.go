package conf

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestApplyDefaultsSetsNodeProfile(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.NodeCfg.Profile != "n42" {
		t.Fatalf("profile = %q, want %q", cfg.NodeCfg.Profile, "n42")
	}
}

func TestValidateRejectsUnknownNodeProfile(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Profile: "mystery",
			Chain:   "private",
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected Validate to reject unknown profile")
	}
}

func TestValidateRejectsUnsupportedChainForProfile(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Profile: "eth",
			Chain:   "mainnet",
		},
		ChainCfg: params.MainnetChainConfig,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected Validate to reject n42 chain for ethereum EL profile")
	}
}

func TestValidateRejectsUnsupportedConsensusForProfile(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Profile: "eth",
			Chain:   "private",
		},
		ChainCfg: &params.ChainConfig{Consensus: params.AposConsensu},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected Validate to reject apos consensus for ethereum EL profile")
	}
}

func TestNormalizeNetworkSelectionCanonicalizesEthereumPreset(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Chain:   "eth-testnet",
			Profile: "eth",
		},
	}

	if err := NormalizeNetworkSelection(cfg); err != nil {
		t.Fatalf("NormalizeNetworkSelection returned error: %v", err)
	}
	if cfg.NodeCfg.Chain != "eth-sepolia" {
		t.Fatalf("chain = %q, want %q", cfg.NodeCfg.Chain, "eth-sepolia")
	}
	if cfg.NodeCfg.Profile != "eth-el" {
		t.Fatalf("profile = %q, want %q", cfg.NodeCfg.Profile, "eth-el")
	}
	if cfg.NodeCfg.JMTCommitment {
		t.Fatal("expected ethereum preset to disable JMT commitment")
	}
	if cfg.ChainCfg != params.EthereumSepoliaChainConfig {
		t.Fatal("expected canonical sepolia chain config")
	}
}

func TestNormalizeNetworkSelectionOverridesCanonicalChainConfig(t *testing.T) {
	cfg := &Config{
		NodeCfg: NodeConfig{
			Chain:   "mainnet",
			Profile: "n42",
		},
		ChainCfg: &params.ChainConfig{Consensus: params.Faker},
	}

	if err := NormalizeNetworkSelection(cfg); err != nil {
		t.Fatalf("NormalizeNetworkSelection returned error: %v", err)
	}
	if cfg.ChainCfg != params.MainnetChainConfig {
		t.Fatal("expected canonical mainnet chain config to override custom config")
	}
	if !cfg.NodeCfg.JMTCommitment {
		t.Fatal("expected n42 canonical mainnet to use JMT commitment")
	}
}
