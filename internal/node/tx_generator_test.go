package node

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

// TestNewNodeStartsAndStopsTxGenerator exercises startTxGenerator: the eth
// profile supports the developer runtime (TestResolveAuxiliaryRuntimePlan*
// in profile_test.go confirms it keeps startTxGenerator enabled), so
// enabling DevCfg.TxGenEnabled on an otherwise-minimal node drives New,
// FundAccounts and Start, and Close drives the generator's Stop path.
func TestNewNodeStartsAndStopsTxGenerator(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	cfg.DevCfg = conf.DevConfig{
		TxGenEnabled:     true,
		TxGenMaxPerBlock: 1,
		TxGenInterval:    20 * time.Millisecond,
		TxGenGasPrice:    1000000000,
		TxGenKey:         "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690",
	}

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}

	if err := n.Start(); err != nil {
		n.Close()
		t.Fatalf("Start failed: %v", err)
	}

	if n.txGenerator == nil {
		t.Fatal("expected startTxGenerator to install a generator")
	}

	// Give the generator's background goroutine a moment to actually enter
	// its select loop before we tear it down.
	time.Sleep(50 * time.Millisecond)

	if err := n.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestNewNodeSkipsTxGeneratorWhenDisabled confirms the plan-gated early
// return in startTxGenerator leaves n.txGenerator nil when the dev config
// does not request it.
func TestNewNodeSkipsTxGeneratorWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if n.txGenerator != nil {
		t.Fatal("expected no tx generator to be installed when TxGenEnabled is false")
	}
}
