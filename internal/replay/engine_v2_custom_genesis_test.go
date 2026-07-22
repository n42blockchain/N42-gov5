// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func TestNewEngineV2CustomGenesisOwnsChainConfig(t *testing.T) {
	oldEthereumTxRoot := block.UseEthereumTxRoot
	defer func() { block.UseEthereumTxRoot = oldEthereumTxRoot }()

	customConfig := *params.MainnetChainConfig
	customConfig.ChainID = big.NewInt(1143)
	customConfig.StateScheme = string(params.StateCommitmentPresetQMDB)
	genesis := &conf.Genesis{Config: &customConfig}

	engine, err := NewEngineV2(ConfigV2{
		SourcePath:  "/tmp/custom-source",
		TargetPath:  "/tmp/custom-target",
		ChainConfig: params.MainnetChainConfig,
		Genesis:     genesis,
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.cfg.ChainConfig != genesis.Config {
		t.Fatal("custom genesis chain config did not override the named/default config")
	}
	if got := engine.cfg.ChainConfig.ChainID.Uint64(); got != 1143 {
		t.Fatalf("chain ID = %d, want 1143", got)
	}
	if !block.UseEthereumTxRoot {
		t.Fatal("QMDB custom chain did not select the Ethereum transaction-root codec")
	}
}

func TestNewEngineV2RejectsCustomGenesisWithoutConfig(t *testing.T) {
	_, err := NewEngineV2(ConfigV2{
		SourcePath: "/tmp/custom-source",
		TargetPath: "/tmp/custom-target",
		Genesis:    &conf.Genesis{},
	})
	if err == nil {
		t.Fatal("custom genesis without chain config was accepted")
	}
}
