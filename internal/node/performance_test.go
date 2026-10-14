package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func performanceGenesisFixture(t *testing.T) *conf.Genesis {
	t.Helper()
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	return &genesis
}

func performanceNodeConfig(t *testing.T, g *conf.Genesis) *conf.Config {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	// Genesis' parser accepts hex bytes; its default struct marshaler emits
	// base64 for []byte. Keep the fixture in the on-disk genesis format.
	var encoded map[string]interface{}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	encoded["extraData"] = fmt.Sprintf("0x%x", g.ExtraData)
	raw, err = json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "chain.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return &conf.Config{NodeCfg: conf.NodeConfig{
		Chain: "private", Profile: "n42", DataDir: filepath.Join(dir, "not-created"), PerformanceGenesis: file,
	}}
}

func TestPerformanceStartupRejectsBeforeOpeningDatabase(t *testing.T) {
	for _, tc := range []struct {
		change func(*conf.Genesis)
		want   string
	}{
		{func(g *conf.Genesis) { g.Config.Performance.Role = "validator" }, "not implemented"},
		{func(g *conf.Genesis) { g.Config.Performance.Mode = "shared-go-E1" }, "not implemented"},
		{func(g *conf.Genesis) { g.Config.Performance = nil }, "explicitly contain"},
		{func(g *conf.Genesis) { g.Timestamp = 1678174066 }, "dedicated"},
		{func(g *conf.Genesis) { g.ExtraData = nil }, "dedicated"},
	} {
		g := performanceGenesisFixture(t)
		tc.change(g)
		cfg := performanceNodeConfig(t, g)
		node, err := NewNode(nodeTNewContext(), cfg)
		if err == nil || node != nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("startup accepted invalid actual genesis: %v", err)
		}
		if _, err := os.Stat(cfg.NodeCfg.DataDir); !os.IsNotExist(err) {
			t.Fatalf("startup touched datadir before rejecting configuration: %v", err)
		}
	}
}

func TestPerformanceActualGenesisSelectionIsExplicit(t *testing.T) {
	g := performanceGenesisFixture(t)
	cfg := performanceNodeConfig(t, g)
	profile, _ := params.ResolveExecutionProfile("n42")
	resolved, err := resolveConfiguredGenesis(cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.isPrivate || !resolved.requiresExplicitInit || resolved.genesisHash == nil || resolved.chainConfig.Performance == nil {
		t.Fatal("explicit performance genesis was replaced by devnet or auto-initialization")
	}
	for _, chain := range []string{"mainnet", "mainnet_qmdb_staggered", "qs_epoch_test", "eth-mainnet"} {
		known := params.GenesisHashByChainName(chain)
		if known == nil || known.Hex()[:10] == resolved.genesisHash.Hex()[:10] {
			t.Fatalf("performance genesis fork digest conflicts with %s", chain)
		}
	}
	if err := ValidatePerformanceGenesis(cfg, resolved.genesis); err != nil {
		t.Fatal(err)
	}
	// A file path is not enough to silently opt a nil-performance genesis in.
	g.Config.Performance = nil
	if err := ValidatePerformanceGenesis(cfg, g); err == nil {
		t.Fatal("implicit performance configuration accepted")
	}
	// A config stanza cannot be silently dropped by private devnet synthesis.
	cfg.NodeCfg.PerformanceGenesis = ""
	cfg.ChainCfg = resolved.chainConfig
	if _, err := resolveConfiguredGenesis(cfg, profile); err == nil {
		t.Fatal("performance config without explicit genesis accepted")
	}
}

func TestPerformanceStartupRequiresExplicitInitBeforeOpeningDatabase(t *testing.T) {
	cfg := performanceNodeConfig(t, performanceGenesisFixture(t))
	node, err := NewNode(nodeTNewContext(), cfg)
	if node != nil || err == nil || !strings.Contains(err.Error(), "n42 init --profile n42") {
		t.Fatalf("startup initialized performance genesis implicitly: %v", err)
	}
	if _, err := os.Stat(cfg.NodeCfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("startup created datadir before explicit init: %v", err)
	}
}

func TestPerformanceGenesisRejectsEthereumAndInitMetadata(t *testing.T) {
	g := performanceGenesisFixture(t)
	cfg := performanceNodeConfig(t, g)
	cfg.NodeCfg.Profile = "eth"
	if err := ValidatePerformanceGenesis(cfg, g); err == nil {
		t.Fatal("Ethereum init accepted performance genesis")
	}
	profile, _ := params.ResolveExecutionProfile("eth")
	if _, err := resolveConfiguredGenesis(cfg, profile); err == nil {
		t.Fatal("Ethereum startup accepted performance genesis")
	}
	cfg.NodeCfg.Profile = "n42"
	cfg.P2PCfg = &conf.P2PConfig{GenesisHashOverride: "0x1234"}
	if err := ValidatePerformanceGenesis(cfg, g); err == nil {
		t.Fatal("performance genesis accepted P2P identity override")
	}
	cfg.P2PCfg = nil
	g.Timestamp++
	if err := ValidatePerformanceGenesis(cfg, g); err == nil {
		t.Fatal("init accepted changed genesis identity")
	}
}

func TestPerformanceManifestRefusesChangeBeforeDatabaseOpen(t *testing.T) {
	g := performanceGenesisFixture(t)
	cfg := performanceNodeConfig(t, g)
	profile, _ := params.ResolveExecutionProfile("n42")
	resolved, err := resolveConfiguredGenesis(cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.NodeCfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := PersistDataDirNetworkBinding(cfg, g.Config, *resolved.genesisHash); err != nil {
		t.Fatal(err)
	}
	if err := validatePerformanceNetworkBinding(cfg, resolved); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*dataDirNetworkManifest){
		func(m *dataDirNetworkManifest) { m.Performance.Role = "validator" },
		func(m *dataDirNetworkManifest) { m.Performance = nil },
		func(m *dataDirNetworkManifest) { m.GenesisHash = types.HexToHash("0x1234").Hex() },
	} {
		manifest, err := buildRequestedNetworkManifest(cfg, g.Config, resolved.genesisHash)
		if err != nil {
			t.Fatal(err)
		}
		copyConfig := *manifest.Performance
		manifest.Performance = &copyConfig
		mutation(&manifest)
		if err := writeDataDirNetworkManifest(cfg.NodeCfg.DataDir, manifest); err != nil {
			t.Fatal(err)
		}
		node, err := NewNode(nodeTNewContext(), cfg)
		if node != nil || !errors.Is(err, ErrDatadirNetworkMismatch) {
			t.Fatalf("startup failed to reject changed binding before MDBX: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cfg.NodeCfg.DataDir, "chaindata")); !os.IsNotExist(err) {
			t.Fatal("opened database before checking binding")
		}
	}
	// Removing the flag must not silently reopen an E1/performance database as
	// ordinary devnet, even if genesis file configuration is no longer supplied.
	if err := PersistDataDirNetworkBinding(cfg, g.Config, *resolved.genesisHash); err == nil {
		// The previous mismatch is deliberately still present and must not be repaired.
		t.Fatal("binding was overwritten")
	}
	cfg.NodeCfg.PerformanceGenesis = ""
	_, err = NewNode(nodeTNewContext(), cfg)
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("performance opt-out drift accepted: %v", err)
	}
}
