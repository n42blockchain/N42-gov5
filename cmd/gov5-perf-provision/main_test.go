package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func provisionTestPaths(t *testing.T) (string, string) {
	t.Helper()
	base, err := os.MkdirTemp("", "gov5-pr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	template, err := filepath.Abs("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	return base, template
}
func TestProvisionWritesSevenPrivateIdentitiesWithoutDatabaseOrProcesses(t *testing.T) {
	base, template := provisionTestPaths(t)
	root := filepath.Join(base, "r")
	if err := provision(root, template, 4, 25000, base); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run manifest
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "provisioned-not-initialized" || len(run.Validators) != 7 || !run.OwnerParallelEVM || run.Transactions != "ethereum-secp256k1" {
		t.Fatal("provisioned status/topology was misrepresented")
	}
	graw, err := os.ReadFile(run.Genesis)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(graw)
	if hex.EncodeToString(sum[:]) != run.GenesisSHA256 {
		t.Fatal("full genesis-file binding differs")
	}
	var g conf.Genesis
	if err := json.Unmarshal(graw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Alloc) != 4 || len(g.Config.HotStuff.Validators) != 7 {
		t.Fatal("genesis lost provisioned public identities/accounts")
	}
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	if gb.Hash() != run.Identity.GenesisHash {
		t.Fatal("genesis header identity differs")
	}
	engine := hotstuff.New(g.Config.HotStuff, g.Config)
	defer engine.Close()
	if err := engine.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	addresses, peers := map[string]bool{}, map[string]bool{}
	for _, v := range run.Validators {
		if addresses[v.Address] || peers[v.PeerID] {
			t.Fatal("identities are not independent")
		}
		addresses[v.Address] = true
		peers[v.PeerID] = true
		var cfg conf.Config
		if err := conf.LoadConfigFromFile(v.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		if err := conf.ValidateAndApplyDefaults(&cfg); err != nil {
			t.Fatal("generated config fails real YAML/default loader:", err)
		}
		if cfg.NodeCfg.PerformanceGenesis != run.Genesis || cfg.NodeCfg.PerformanceRole != "validator" || cfg.NodeCfg.PerformanceExecutionSocket != run.OwnerSocket || cfg.NodeCfg.DataDir != v.DataDir || !cfg.NodeCfg.Miner || cfg.Miner.Etherbase != v.Address || cfg.Miner.BlockIntervalMs != 2000 || cfg.NodeCfg.IPCPath != "" || cfg.NodeCfg.HTTP || cfg.NodeCfg.AuthRPC || cfg.NodeCfg.WS {
			t.Fatal("generated runtime selection was lost by the real config loader")
		}
		if !cfg.P2PCfg.NoDiscovery || cfg.P2PCfg.QUICEnabled || len(cfg.P2PCfg.StaticPeers) != 6 || len(cfg.P2PCfg.BootstrapNodeAddr) != 0 || cfg.P2PCfg.LocalIP != "127.0.0.1" {
			t.Fatal("generated network enables public or non-explicit peers")
		}
		key, err := hotstuff.LoadBLSKeyFromDir(filepath.Join(v.DataDir, "keystore"), types.HexToAddress(v.Address))
		if err != nil {
			t.Fatal(err)
		}
		public, err := hex.DecodeString(g.Config.HotStuff.Validators[v.Index].BLSKey[2:])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(key.PublicKey().Marshal(), public) {
			t.Fatal("validator key is not bound to common genesis")
		}
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0700 {
				t.Fatal("runtime directory is not private")
			}
		} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("provisioning created public file/socket")
		}
		if info.Name() == "chaindata" {
			t.Fatal("provisioning initialized chain data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(run.OwnerDir, "keystore")); !os.IsNotExist(err) {
		t.Fatal("executor received validator keystore")
	}
	marker := filepath.Join(run.OwnerDir, "keep-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := provision(root, template, 4, 25000, base); err == nil {
		t.Fatal("existing run/key material was overwritten")
	}
	if value, err := os.ReadFile(marker); err != nil || string(value) != "keep" {
		t.Fatal("existing runtime was modified")
	}
}
func TestProvisionRejectsProtectedRootsAndSymlinksBeforeWriting(t *testing.T) {
	base, template := provisionTestPaths(t)
	for _, root := range []string{"relative", base, "/data/blockchain/qs-node0", "/home/n42/src/n42/N42-gov5/n42data"} {
		if err := provision(root, template, 4, 25000, base); err == nil {
			t.Fatal("unisolated runtime root accepted")
		}
	}
	outside := t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := provision(filepath.Join(alias, "new", "r"), template, 4, 25000, base); err == nil {
		t.Fatal("symlink escaped runtime root")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("rejected symlink path was mutated")
	}
}
