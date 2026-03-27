package node

import (
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/params/networkname"
)

func TestNodeProfileReturnsResolvedDescriptor(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}

	n := &Node{profile: profile}
	if got := n.Profile(); got.Name() != params.ExecutionProfileEthereumEL {
		t.Fatalf("profile name = %q, want %q", got.Name(), params.ExecutionProfileEthereumEL)
	}
}

func TestResolveConfiguredGenesisPrivateChainUsesDevnet(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	resolved, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: "private"}}, profile)
	if err != nil {
		t.Fatalf("resolveConfiguredGenesis returned error: %v", err)
	}
	if !resolved.isPrivate {
		t.Fatal("expected private chain resolution")
	}
	if resolved.genesis == nil || resolved.chainConfig == nil {
		t.Fatal("expected devnet genesis and chain config")
	}
	if resolved.genesisHash != nil {
		t.Fatal("expected private chain not to expose canonical genesis hash")
	}
}

func TestResolveConfiguredGenesisKnownChainUsesCanonicalFixtures(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("n42")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	resolved, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: networkname.MainnetChainName}}, profile)
	if err != nil {
		t.Fatalf("resolveConfiguredGenesis returned error: %v", err)
	}
	if resolved.isPrivate {
		t.Fatal("expected non-private chain resolution")
	}
	if resolved.genesis == nil || resolved.chainConfig == nil || resolved.genesisHash == nil {
		t.Fatal("expected canonical genesis, chain config, and hash")
	}
	if *resolved.genesisHash != params.MainnetGenesisHash {
		t.Fatalf("genesis hash = %s, want %s", *resolved.genesisHash, params.MainnetGenesisHash)
	}
	if resolved.chainConfig != params.MainnetChainConfig {
		t.Fatal("expected mainnet chain config")
	}
	if canonicalHash, ok := resolved.canonicalHash(); !ok || canonicalHash != params.MainnetGenesisHash {
		t.Fatalf("canonical hash = %s, ok=%v, want %s", canonicalHash, ok, params.MainnetGenesisHash)
	}
}

func TestResolveConfiguredGenesisRejectsUnknownChain(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("n42")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if _, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: "mystery"}}, profile); err == nil {
		t.Fatal("expected unknown chain to be rejected")
	}
}

func TestResolveConfiguredGenesisPrivateChainHasNoCanonicalHash(t *testing.T) {
	resolved := configuredGenesis{isPrivate: true}
	if _, ok := resolved.canonicalHash(); ok {
		t.Fatal("expected private chain to have no canonical hash")
	}
}

func TestResolveConfiguredGenesisRejectsN42ChainForEthereumEL(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if _, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: networkname.MainnetChainName}}, profile); err == nil {
		t.Fatal("expected ethereum EL profile to reject n42 canonical chain")
	}
}

func TestResolveConsensusEngineRejectsMissingChainConfig(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("n42")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if _, err := resolveConsensusEngine(&conf.Config{}, profile, nil); err == nil || err.Error() != "missing chain config" {
		t.Fatalf("resolveConsensusEngine error = %v, want missing chain config", err)
	}
}

func TestResolveConsensusEngineReturnsFaker(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	engine, err := resolveConsensusEngine(&conf.Config{
		ChainCfg: &params.ChainConfig{Consensus: params.Faker},
	}, profile, nil)
	if err != nil {
		t.Fatalf("resolveConsensusEngine returned error: %v", err)
	}
	if got := engine.Type(); got != params.Faker {
		t.Fatalf("engine type = %q, want %q", got, params.Faker)
	}
}

func TestResolveConsensusEngineRejectsUnknownConsensus(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("n42")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if _, err := resolveConsensusEngine(&conf.Config{
		ChainCfg: &params.ChainConfig{Consensus: params.ConsensusType("mystery")},
	}, profile, nil); err == nil {
		t.Fatal("expected unknown consensus to be rejected")
	}
}

func TestResolveConsensusEngineRejectsN42OnlyConsensusForEthereumEL(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	if _, err := resolveConsensusEngine(&conf.Config{
		ChainCfg: &params.ChainConfig{Consensus: params.AposConsensu},
	}, profile, nil); err == nil {
		t.Fatal("expected ethereum EL profile to reject apos consensus")
	}
}

func TestResolveAuxiliaryRuntimePlanN42KeepsConfiguredRuntimes(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("n42")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}

	cfg := &conf.Config{}
	cfg.MCPCfg.Enabled = true
	cfg.Web3GatewayCfg.Enabled = true
	cfg.IngestCfg.Enabled = true
	cfg.DevCfg.TxGenEnabled = true
	cfg.AICfg.Wallet.Enabled = true
	cfg.CoprocessorCfg.Enabled = true
	cfg.BridgeCfg.Enabled = true

	plan := resolveAuxiliaryRuntimePlan(cfg, profile)
	if !plan.startMCPServer || !plan.startWeb3Gateway || !plan.startIngestServer || !plan.startTxGenerator {
		t.Fatal("expected n42 profile to keep auxiliary runtime plan enabled")
	}
	if !plan.startAIRuntime || !plan.startDistributed || !plan.startBridgeRuntime {
		t.Fatal("expected n42 profile to keep n42 runtime plan enabled")
	}
	if len(plan.disabledByProfile) != 0 {
		t.Fatalf("disabledByProfile = %v, want empty", plan.disabledByProfile)
	}
}

func TestResolveAuxiliaryRuntimePlanEthereumELFiltersN42Runtimes(t *testing.T) {
	profile, err := params.ResolveExecutionProfile("eth")
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}

	cfg := &conf.Config{}
	cfg.MCPCfg.Enabled = true
	cfg.Web3GatewayCfg.Enabled = true
	cfg.IngestCfg.Enabled = true
	cfg.DevCfg.TxGenEnabled = true
	cfg.AICfg.Wallet.Enabled = true
	cfg.CoprocessorCfg.Enabled = true
	cfg.BridgeCfg.Enabled = true

	plan := resolveAuxiliaryRuntimePlan(cfg, profile)
	if plan.startMCPServer || plan.startAIRuntime || plan.startDistributed || plan.startBridgeRuntime {
		t.Fatal("expected ethereum EL profile to filter n42-only runtimes")
	}
	if !plan.startWeb3Gateway || !plan.startIngestServer || !plan.startTxGenerator {
		t.Fatal("expected ethereum EL profile to keep generic/developer runtimes")
	}
	if len(plan.disabledByProfile) != 4 {
		t.Fatalf("disabledByProfile len = %d, want 4", len(plan.disabledByProfile))
	}
}
