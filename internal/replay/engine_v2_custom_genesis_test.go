// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
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

func TestEngineV2BlockOneReadsGenesisAsParent(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	genesisHash := types.HexToHash("0xb71c8bf2f99a83f37b4e6ea52c5acd940f0251bd5d0ee7694e6a03ced82092ec")
	if err := rawdb.WriteCanonicalHash(tx, genesisHash, 0); err != nil {
		t.Fatal(err)
	}

	engine := &EngineV2{}
	parentHash, parentTime, err := engine.readParentInfo(tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if parentHash != genesisHash {
		t.Fatalf("block 1 parent = %s, want genesis %s", parentHash, genesisHash)
	}
	if parentTime != 0 {
		t.Fatalf("genesis timestamp = %d without stored block, want 0", parentTime)
	}
}
