package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/params"
)

// ValidatePerformanceGenesis checks the actual genesis, including header
// metadata that chain ID alone cannot bind. Init and startup call it before
// opening any database. It leaves all legacy nil-performance genesis unchanged.
func ValidatePerformanceGenesis(cfg *conf.Config, genesis *conf.Genesis) error {
	if cfg == nil {
		return errors.New("missing node configuration")
	}
	if genesis == nil || genesis.Config == nil {
		return errors.New("missing genesis configuration")
	}
	if genesis.Config.Performance == nil {
		if cfg.NodeCfg.PerformanceGenesis != "" {
			return errors.New("performance genesis must explicitly contain performance configuration")
		}
		return nil
	}
	profile, err := params.ResolveExecutionProfile(cfg.NodeCfg.Profile)
	if err != nil {
		return err
	}
	if err := genesis.Config.ValidatePerformanceSelection(cfg.NodeCfg.Chain, profile); err != nil {
		return err
	}
	if cfg.P2PCfg != nil && cfg.P2PCfg.GenesisHashOverride != "" {
		return errors.New("performance genesis cannot override the P2P genesis identity")
	}
	if err := p2p.ValidatePerformanceP2PConfig(cfg.P2PCfg); err != nil {
		return err
	}
	if genesis.Timestamp != params.PerformanceGenesisTime ||
		!bytes.Equal(genesis.ExtraData, []byte(params.PerformanceGenesisMarker)) || genesis.Number != 0 {
		return errors.New("performance genesis requires its dedicated timestamp, extraData marker and block zero")
	}
	return nil
}

func resolvePerformanceGenesis(cfg *conf.Config, profile params.ProfileDescriptor) (configuredGenesis, error) {
	if cfg.NodeCfg.Chain != "private" || !profile.IsN42() {
		return configuredGenesis{}, errors.New("performance genesis requires private chain and n42 profile")
	}
	if !filepath.IsAbs(cfg.NodeCfg.PerformanceGenesis) {
		return configuredGenesis{}, errors.New("performance genesis path must be absolute")
	}
	raw, err := os.ReadFile(cfg.NodeCfg.PerformanceGenesis)
	if err != nil {
		return configuredGenesis{}, err
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return configuredGenesis{}, fmt.Errorf("decode performance genesis: %w", err)
	}
	if err := ValidatePerformanceGenesis(cfg, &genesis); err != nil {
		return configuredGenesis{}, err
	}
	if cfg.ChainCfg != nil && cfg.ChainCfg.Performance != nil {
		if err := cfg.ChainCfg.CheckCompatible(genesis.Config, 0); err != nil {
			return configuredGenesis{}, err
		}
	}
	blk, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil {
		return configuredGenesis{}, err
	}
	hash := blk.Hash()
	return configuredGenesis{
		genesis: &genesis, chainConfig: genesis.Config, genesisHash: &hash,
		isPrivate: true, requiresExplicitInit: true,
	}, nil
}

// Read only the existing manifest here. Legacy private genesis resolution
// remains unchanged; performance opt-in/removal must be checked before MDBX.
func validatePerformanceNetworkBinding(cfg *conf.Config, genesis configuredGenesis) error {
	if cfg.NodeCfg.DataDir == "" {
		if genesis.chainConfig.Performance != nil {
			return errors.New("performance chain requires a persistent dedicated datadir initialized with n42 init")
		}
		return nil
	}
	manifest, err := readDataDirNetworkManifest(cfg.NodeCfg.DataDir)
	if err != nil {
		return err
	}
	if genesis.chainConfig.Performance != nil && manifest == nil {
		return performanceInitRequiredError(cfg)
	}
	if genesis.chainConfig.Performance == nil && (manifest == nil || manifest.Performance == nil) {
		return nil
	}
	return ValidateDataDirNetworkBinding(cfg, genesis.chainConfig, genesis.genesisHash)
}

func performanceInitRequiredError(cfg *conf.Config) error {
	return fmt.Errorf("performance chain requires explicit genesis initialization; run n42 init --profile n42 --chain private --data.dir %q %q first", cfg.NodeCfg.DataDir, cfg.NodeCfg.PerformanceGenesis)
}
