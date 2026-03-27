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
