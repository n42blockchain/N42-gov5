package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

const (
	dataDirNetworkManifestFilename = "network.json"
	dataDirNetworkManifestVersion  = 1
)

type dataDirNetworkManifest struct {
	Version     int    `json:"version"`
	Chain       string `json:"chain"`
	Profile     string `json:"profile"`
	Commitment  string `json:"commitment"`
	ChainID     string `json:"chain_id,omitempty"`
	Consensus   string `json:"consensus,omitempty"`
	GenesisHash string `json:"genesis_hash,omitempty"`
}

func ValidateDataDirNetworkBinding(cfg *conf.Config, chainCfg *params.ChainConfig, genesisHash *types.Hash) error {
	if cfg == nil || cfg.NodeCfg.DataDir == "" {
		return nil
	}
	requested, err := buildRequestedNetworkManifest(cfg, chainCfg, genesisHash)
	if err != nil {
		return err
	}
	stored, err := readDataDirNetworkManifest(cfg.NodeCfg.DataDir)
	if err != nil || stored == nil {
		return err
	}
	return compareDataDirNetworkManifest(cfg.NodeCfg.DataDir, *stored, requested)
}

func PersistDataDirNetworkBinding(cfg *conf.Config, chainCfg *params.ChainConfig, genesisHash types.Hash) error {
	if cfg == nil || cfg.NodeCfg.DataDir == "" {
		return nil
	}
	requested, err := buildRequestedNetworkManifest(cfg, chainCfg, &genesisHash)
	if err != nil {
		return err
	}
	stored, err := readDataDirNetworkManifest(cfg.NodeCfg.DataDir)
	if err != nil {
		return err
	}
	if stored != nil {
		return compareDataDirNetworkManifest(cfg.NodeCfg.DataDir, *stored, requested)
	}
	return writeDataDirNetworkManifest(cfg.NodeCfg.DataDir, requested)
}

func ensureDataDirNetworkBinding(cfg *conf.Config, chainCfg *params.ChainConfig, genesisHash types.Hash) error {
	if cfg == nil || cfg.NodeCfg.DataDir == "" {
		return nil
	}
	requested, err := buildRequestedNetworkManifest(cfg, chainCfg, &genesisHash)
	if err != nil {
		return err
	}
	stored, err := readDataDirNetworkManifest(cfg.NodeCfg.DataDir)
	if err != nil {
		return err
	}
	if stored != nil {
		return compareDataDirNetworkManifest(cfg.NodeCfg.DataDir, *stored, requested)
	}
	actual, err := inferActualNetworkManifest(cfg, chainCfg, genesisHash)
	if err != nil {
		return err
	}
	if err := compareDataDirNetworkManifest(cfg.NodeCfg.DataDir, actual, requested); err != nil {
		return err
	}
	return writeDataDirNetworkManifest(cfg.NodeCfg.DataDir, actual)
}

func buildRequestedNetworkManifest(cfg *conf.Config, chainCfg *params.ChainConfig, genesisHash *types.Hash) (dataDirNetworkManifest, error) {
	if cfg == nil {
		return dataDirNetworkManifest{}, errors.New("missing config")
	}
	preset, err := params.ResolveNetworkPreset(cfg.NodeCfg.Chain, cfg.NodeCfg.Profile)
	if err != nil {
		return dataDirNetworkManifest{}, err
	}
	return buildNetworkManifestFromPreset(preset, chainCfg, genesisHash)
}

func inferActualNetworkManifest(cfg *conf.Config, chainCfg *params.ChainConfig, genesisHash types.Hash) (dataDirNetworkManifest, error) {
	if preset, ok := params.InferNetworkPresetFromChainConfig(chainCfg); ok {
		return buildNetworkManifestFromPreset(preset, chainCfg, &genesisHash)
	}
	return buildRequestedNetworkManifest(cfg, chainCfg, &genesisHash)
}

func buildNetworkManifestFromPreset(preset params.NetworkPreset, chainCfg *params.ChainConfig, genesisHash *types.Hash) (dataDirNetworkManifest, error) {
	if chainCfg == nil {
		return dataDirNetworkManifest{}, errors.New("missing chain config")
	}
	manifest := dataDirNetworkManifest{
		Version:    dataDirNetworkManifestVersion,
		Chain:      preset.Chain,
		Profile:    preset.Profile.String(),
		Commitment: string(preset.Commitment),
		Consensus:  string(chainCfg.Consensus),
	}
	if chainCfg.ChainID != nil {
		manifest.ChainID = chainCfg.ChainID.String()
	}
	if preset.Chain == "private" && genesisHash != nil && *genesisHash != (types.Hash{}) {
		manifest.GenesisHash = genesisHash.Hex()
	}
	return manifest, nil
}

func compareDataDirNetworkManifest(dataDir string, stored, requested dataDirNetworkManifest) error {
	if stored.Version != dataDirNetworkManifestVersion {
		return fmt.Errorf("%w: datadir %q has unsupported network manifest version %d", ErrDatadirNetworkMismatch, dataDir, stored.Version)
	}
	if stored.Chain != requested.Chain ||
		stored.Profile != requested.Profile ||
		stored.Commitment != requested.Commitment ||
		stored.ChainID != requested.ChainID ||
		stored.Consensus != requested.Consensus ||
		(stored.GenesisHash != "" && requested.GenesisHash != "" && stored.GenesisHash != requested.GenesisHash) {
		return fmt.Errorf(
			"%w: datadir %q is bound to %s, requested %s",
			ErrDatadirNetworkMismatch,
			dataDir,
			stored.summary(),
			requested.summary(),
		)
	}
	return nil
}

func (m dataDirNetworkManifest) summary() string {
	parts := []string{
		"chain=" + quoteIfEmpty(m.Chain),
		"profile=" + quoteIfEmpty(m.Profile),
		"commitment=" + quoteIfEmpty(m.Commitment),
	}
	if m.ChainID != "" {
		parts = append(parts, "chain_id="+m.ChainID)
	}
	if m.Consensus != "" {
		parts = append(parts, "consensus="+m.Consensus)
	}
	if m.GenesisHash != "" {
		parts = append(parts, "genesis_hash="+m.GenesisHash)
	}
	return strings.Join(parts, ", ")
}

func readDataDirNetworkManifest(dataDir string) (*dataDirNetworkManifest, error) {
	raw, err := os.ReadFile(dataDirNetworkManifestPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest dataDirNetworkManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode datadir network manifest: %w", err)
	}
	return &manifest, nil
}

func writeDataDirNetworkManifest(dataDir string, manifest dataDirNetworkManifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(dataDirNetworkManifestPath(dataDir), raw, 0o644)
}

func dataDirNetworkManifestPath(dataDir string) string {
	return filepath.Join(dataDir, dataDirNetworkManifestFilename)
}

func quoteIfEmpty(value string) string {
	if value == "" {
		return `""`
	}
	return value
}
