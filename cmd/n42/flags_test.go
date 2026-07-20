package main

import (
	"flag"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/params"
)

func TestQuickStartDevSetsN42PrivateMode(t *testing.T) {
	resetQuickStartDefaultsForTest(t)

	if err := runQuickStartBoolFlag(t, "dev", true); err != nil {
		t.Fatalf("runQuickStartBoolFlag returned error: %v", err)
	}
	if DefaultConfig.NodeCfg.Chain != "private" {
		t.Fatalf("chain = %q, want %q", DefaultConfig.NodeCfg.Chain, "private")
	}
	if DefaultConfig.NodeCfg.Profile != "n42" {
		t.Fatalf("profile = %q, want %q", DefaultConfig.NodeCfg.Profile, "n42")
	}
	if !DefaultConfig.P2PCfg.NoDiscovery {
		t.Fatal("expected no discovery in --dev mode")
	}
	if DefaultConfig.P2PCfg.MaxPeers != 0 {
		t.Fatalf("max peers = %d, want 0", DefaultConfig.P2PCfg.MaxPeers)
	}
	if DefaultConfig.Miner.GasCeil != params.DevTestGasLimit {
		t.Fatalf("gas ceiling = %d, want %d", DefaultConfig.Miner.GasCeil, params.DevTestGasLimit)
	}
}

func TestQuickStartEthDevSetsEthereumPrivateMode(t *testing.T) {
	resetQuickStartDefaultsForTest(t)

	if err := runQuickStartBoolFlag(t, "ethdev", true); err != nil {
		t.Fatalf("runQuickStartBoolFlag returned error: %v", err)
	}
	if DefaultConfig.NodeCfg.Chain != "private" {
		t.Fatalf("chain = %q, want %q", DefaultConfig.NodeCfg.Chain, "private")
	}
	if DefaultConfig.NodeCfg.Profile != "eth" {
		t.Fatalf("profile = %q, want %q", DefaultConfig.NodeCfg.Profile, "eth")
	}
	if !DefaultConfig.P2PCfg.NoDiscovery {
		t.Fatal("expected no discovery in --ethdev mode")
	}
	if DefaultConfig.P2PCfg.MaxPeers != 0 {
		t.Fatalf("max peers = %d, want 0", DefaultConfig.P2PCfg.MaxPeers)
	}
	if DefaultConfig.Miner.GasCeil != params.DevTestGasLimit {
		t.Fatalf("gas ceiling = %d, want %d", DefaultConfig.Miner.GasCeil, params.DevTestGasLimit)
	}
}

func resetQuickStartDefaultsForTest(t *testing.T) {
	t.Helper()
	originalChain := DefaultConfig.NodeCfg.Chain
	originalProfile := DefaultConfig.NodeCfg.Profile
	originalNoDiscovery := DefaultConfig.P2PCfg.NoDiscovery
	originalMaxPeers := DefaultConfig.P2PCfg.MaxPeers
	originalGasCeil := DefaultConfig.Miner.GasCeil
	t.Cleanup(func() {
		DefaultConfig.NodeCfg.Chain = originalChain
		DefaultConfig.NodeCfg.Profile = originalProfile
		DefaultConfig.P2PCfg.NoDiscovery = originalNoDiscovery
		DefaultConfig.P2PCfg.MaxPeers = originalMaxPeers
		DefaultConfig.Miner.GasCeil = originalGasCeil
	})

	DefaultConfig.NodeCfg.Chain = "mainnet"
	DefaultConfig.NodeCfg.Profile = ""
	DefaultConfig.P2PCfg.NoDiscovery = false
	DefaultConfig.P2PCfg.MaxPeers = DefaultMaxPeers
	DefaultConfig.Miner.GasCeil = 0
}

func runQuickStartBoolFlag(t *testing.T, name string, value bool) error {
	t.Helper()

	boolFlag := quickStartBoolFlagByName(t, name)
	return boolFlag.Action(newQuickStartTestContext(t), value)
}

func quickStartBoolFlagByName(t *testing.T, name string) *cli.BoolFlag {
	t.Helper()

	for _, cliFlag := range QuickStartFlags {
		if boolFlag, ok := cliFlag.(*cli.BoolFlag); ok && boolFlag.Name == name {
			return boolFlag
		}
	}
	t.Fatalf("quick start bool flag %q not found", name)
	return nil
}

func newQuickStartTestContext(t *testing.T) *cli.Context {
	t.Helper()

	set := flag.NewFlagSet("quick-start-test", flag.ContinueOnError)
	return cli.NewContext(cli.NewApp(), set, nil)
}
