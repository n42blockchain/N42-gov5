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
	resolved, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: "private"}})
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
	resolved, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: networkname.MainnetChainName}})
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
}

func TestResolveConfiguredGenesisRejectsUnknownChain(t *testing.T) {
	if _, err := resolveConfiguredGenesis(&conf.Config{NodeCfg: conf.NodeConfig{Chain: "mystery"}}); err == nil {
		t.Fatal("expected unknown chain to be rejected")
	}
}
