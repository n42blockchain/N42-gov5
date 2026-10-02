package node

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

// nmNoopConfigs covers the shared "cfg == nil or empty DataDir is a no-op"
// guard in ValidateDataDirNetworkBinding, PersistDataDirNetworkBinding and
// ensureDataDirNetworkBinding.
func TestNetworkManifestNoopWhenDataDirOrConfigMissing(t *testing.T) {
	genesisHash := types.HexToHash("0xabc")
	chainCfg := &params.ChainConfig{ChainID: big.NewInt(1), Consensus: params.Faker}

	if err := ValidateDataDirNetworkBinding(nil, chainCfg, &genesisHash); err != nil {
		t.Fatalf("ValidateDataDirNetworkBinding(nil cfg) = %v, want nil", err)
	}
	if err := PersistDataDirNetworkBinding(nil, chainCfg, genesisHash); err != nil {
		t.Fatalf("PersistDataDirNetworkBinding(nil cfg) = %v, want nil", err)
	}
	if err := ensureDataDirNetworkBinding(nil, chainCfg, genesisHash); err != nil {
		t.Fatalf("ensureDataDirNetworkBinding(nil cfg) = %v, want nil", err)
	}

	emptyDirCfg := &conf.Config{NodeCfg: conf.NodeConfig{Chain: "private", Profile: "eth"}}
	if err := ValidateDataDirNetworkBinding(emptyDirCfg, chainCfg, &genesisHash); err != nil {
		t.Fatalf("ValidateDataDirNetworkBinding(empty datadir) = %v, want nil", err)
	}
	if err := PersistDataDirNetworkBinding(emptyDirCfg, chainCfg, genesisHash); err != nil {
		t.Fatalf("PersistDataDirNetworkBinding(empty datadir) = %v, want nil", err)
	}
	if err := ensureDataDirNetworkBinding(emptyDirCfg, chainCfg, genesisHash); err != nil {
		t.Fatalf("ensureDataDirNetworkBinding(empty datadir) = %v, want nil", err)
	}
}

// TestNetworkManifestPropagatesPresetResolutionError exercises the error
// path where ResolveNetworkPreset rejects an inconsistent chain/profile
// pairing, which buildRequestedNetworkManifest surfaces unchanged.
func TestNetworkManifestPropagatesPresetResolutionError(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			DataDir: tmpDir,
			Chain:   "mainnet", // requires the n42 profile
			Profile: "eth",
		},
	}
	chainCfg := &params.ChainConfig{ChainID: big.NewInt(1), Consensus: params.Faker}
	genesisHash := types.HexToHash("0x1")

	if err := ValidateDataDirNetworkBinding(cfg, chainCfg, &genesisHash); err == nil {
		t.Fatal("expected ValidateDataDirNetworkBinding to surface a preset resolution error")
	}
	if err := PersistDataDirNetworkBinding(cfg, chainCfg, genesisHash); err == nil {
		t.Fatal("expected PersistDataDirNetworkBinding to surface a preset resolution error")
	}
}

// TestCompareDataDirNetworkManifestRejectsUnsupportedVersion exercises the
// stored-manifest version guard in compareDataDirNetworkManifest directly.
func TestCompareDataDirNetworkManifestRejectsUnsupportedVersion(t *testing.T) {
	stored := dataDirNetworkManifest{Version: dataDirNetworkManifestVersion + 1, Chain: "private"}
	requested := dataDirNetworkManifest{Version: dataDirNetworkManifestVersion, Chain: "private"}
	err := compareDataDirNetworkManifest("/tmp/x", stored, requested)
	if err == nil {
		t.Fatal("expected an unsupported-version error")
	}
}

// TestReadDataDirNetworkManifestRejectsCorruptJSON exercises the decode
// error branch of readDataDirNetworkManifest against a manifest file that
// isn't valid JSON.
func TestReadDataDirNetworkManifestRejectsCorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := dataDirNetworkManifestPath(tmpDir)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}
	if _, err := readDataDirNetworkManifest(tmpDir); err == nil {
		t.Fatal("expected a decode error for corrupt manifest JSON")
	}
}

// TestValidateDataDirNetworkBindingNoStoredManifestIsNoop exercises
// ValidateDataDirNetworkBinding's path where no manifest has been written
// yet (readDataDirNetworkManifest returns nil, nil).
func TestValidateDataDirNetworkBindingNoStoredManifestIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: tmpDir, Chain: "private", Profile: "eth"},
	}
	chainCfg := &params.ChainConfig{ChainID: big.NewInt(1), Consensus: params.Faker}
	genesisHash := types.HexToHash("0x1")

	if err := ValidateDataDirNetworkBinding(cfg, chainCfg, &genesisHash); err != nil {
		t.Fatalf("ValidateDataDirNetworkBinding with no stored manifest = %v, want nil", err)
	}
	if _, err := os.Stat(dataDirNetworkManifestPath(tmpDir)); !os.IsNotExist(err) {
		t.Fatal("ValidateDataDirNetworkBinding must not write a manifest itself")
	}
}

// TestPersistDataDirNetworkBindingMatchesExistingManifest exercises the
// branch in PersistDataDirNetworkBinding where a manifest already exists and
// matches the requested one: it must succeed without rewriting the file.
func TestPersistDataDirNetworkBindingMatchesExistingManifest(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{DataDir: tmpDir, Chain: "private", Profile: "eth"},
	}
	chainCfg := &params.ChainConfig{ChainID: big.NewInt(1337), Consensus: params.Faker}
	genesisHash := types.HexToHash("0xdead")

	if err := PersistDataDirNetworkBinding(cfg, chainCfg, genesisHash); err != nil {
		t.Fatalf("first PersistDataDirNetworkBinding: %v", err)
	}
	before, err := os.ReadFile(dataDirNetworkManifestPath(tmpDir))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	if err := PersistDataDirNetworkBinding(cfg, chainCfg, genesisHash); err != nil {
		t.Fatalf("second PersistDataDirNetworkBinding (matching) returned error: %v", err)
	}
	after, err := os.ReadFile(dataDirNetworkManifestPath(tmpDir))
	if err != nil {
		t.Fatalf("read manifest again: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("expected manifest contents to be unchanged on a matching persist")
	}
}

// TestDataDirNetworkManifestPathJoinsFilename is a trivial sanity check on
// the path helper used throughout this file.
func TestDataDirNetworkManifestPathJoinsFilename(t *testing.T) {
	got := dataDirNetworkManifestPath("/some/dir")
	want := filepath.Join("/some/dir", "network.json")
	if got != want {
		t.Fatalf("dataDirNetworkManifestPath = %q, want %q", got, want)
	}
}
