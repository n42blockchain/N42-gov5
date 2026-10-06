package node

import (
	"testing"

	"github.com/n42blockchain/N42/conf"
)

// nodeTAIAndMetricsConfig builds an N42-profile private-chain config (AI
// runtimes require profile.SupportsAIRuntime(), true only for N42) with
// every AI subsystem enabled plus a metrics HTTP endpoint on an ephemeral
// loopback port.
func nodeTAIAndMetricsConfig(t *testing.T, dataDir string) *conf.Config {
	cfg := nodeTMCPConfig(dataDir, 0)
	cfg.MCPCfg.Enabled = false

	cfg.AICfg.Wallet.Enabled = true
	cfg.AICfg.Wallet.MaxSessionKeys = 4
	cfg.AICfg.Governance.Enabled = true
	cfg.AICfg.Governance.MaxDatasets = 10
	cfg.AICfg.Governance.CommitteeQuorum = 1
	cfg.AICfg.Governance.CommitteeThreshold = 0.5
	cfg.AICfg.Training.Enabled = true
	cfg.AICfg.Attestation.Enabled = true
	cfg.AICfg.Attestation.TTLSec = 60
	cfg.AICfg.Coord.Enabled = true
	cfg.AICfg.Coord.MaxAgentsPerNode = 10
	cfg.AICfg.Inference.Enabled = true
	cfg.AICfg.Inference.FuncName = "infer"
	cfg.AICfg.Inference.FuelLimit = 1_000_000

	cfg.MetricsCfg.Enable = true
	cfg.MetricsCfg.HTTP = "127.0.0.1"
	cfg.MetricsCfg.Port = nodeTFreeLoopbackPort(t)

	return cfg
}

func TestNewNodeAIRuntimeAndMetricsEnabled(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTAIAndMetricsConfig(t, dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if !n.runtimePlan.startAIRuntime {
		t.Fatal("expected AI runtime to be enabled for the N42 profile")
	}

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if n.walletService == nil {
		t.Fatal("expected wallet service to be constructed")
	}
	if n.dataGovernance == nil {
		t.Fatal("expected data governance committee to be constructed")
	}
	if n.trainingProver == nil {
		t.Fatal("expected training prover to be constructed")
	}
	if n.attestationService == nil {
		t.Fatal("expected attestation service to be constructed")
	}
	if n.agentRegistry == nil {
		t.Fatal("expected agent registry to be constructed")
	}
	if n.inferenceExecutor == nil {
		t.Fatal("expected AI inference executor to be constructed")
	}
}
