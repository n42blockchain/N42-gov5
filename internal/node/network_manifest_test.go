package node

import (
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func TestPersistDataDirNetworkBindingWritesPrivateGenesisHash(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "private",
			Profile: "eth",
		},
	}
	chainCfg := &params.ChainConfig{
		ChainID:   big.NewInt(1337),
		Consensus: params.Faker,
	}
	genesisHash := types.HexToHash("0x1234")

	if err := PersistDataDirNetworkBinding(cfg, chainCfg, genesisHash); err != nil {
		t.Fatalf("PersistDataDirNetworkBinding returned error: %v", err)
	}

	manifest, err := readDataDirNetworkManifest(tmpDir)
	if err != nil {
		t.Fatalf("readDataDirNetworkManifest returned error: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest to be written")
	}
	if manifest.GenesisHash != genesisHash.Hex() {
		t.Fatalf("genesis hash = %q, want %q", manifest.GenesisHash, genesisHash.Hex())
	}
	if err := ValidateDataDirNetworkBinding(cfg, chainCfg, &genesisHash); err != nil {
		t.Fatalf("ValidateDataDirNetworkBinding returned error: %v", err)
	}
}

func TestValidateDataDirNetworkBindingRejectsMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "private",
			Profile: "eth",
		},
	}
	privateCfg := &params.ChainConfig{
		ChainID:   big.NewInt(1337),
		Consensus: params.Faker,
	}
	if err := PersistDataDirNetworkBinding(cfg, privateCfg, types.HexToHash("0x1234")); err != nil {
		t.Fatalf("PersistDataDirNetworkBinding returned error: %v", err)
	}

	mismatchCfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "mainnet",
			Profile: "n42",
		},
	}
	err := ValidateDataDirNetworkBinding(mismatchCfg, params.MainnetChainConfig, nil)
	if !errors.Is(err, ErrDatadirNetworkMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrDatadirNetworkMismatch)
	}
}

func TestEnsureDataDirNetworkBindingInfersAndWritesManifest(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "mainnet",
			Profile: "n42",
		},
	}

	if err := ensureDataDirNetworkBinding(cfg, params.MainnetChainConfig, params.MainnetGenesisHash); err != nil {
		t.Fatalf("ensureDataDirNetworkBinding returned error: %v", err)
	}

	manifest, err := readDataDirNetworkManifest(tmpDir)
	if err != nil {
		t.Fatalf("readDataDirNetworkManifest returned error: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest to be written")
	}
	if manifest.Chain != "mainnet" {
		t.Fatalf("chain = %q, want %q", manifest.Chain, "mainnet")
	}
}

func TestEnsureDataDirNetworkBindingRejectsWrongRequestedPreset(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "testnet",
			Profile: "n42",
		},
	}

	err := ensureDataDirNetworkBinding(cfg, params.MainnetChainConfig, params.MainnetGenesisHash)
	if !errors.Is(err, ErrDatadirNetworkMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrDatadirNetworkMismatch)
	}
}
