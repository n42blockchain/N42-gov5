// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/params"
)

// LoadGenesisChainConfig reads a genesis file's chain config the way `n42
// init` does (cmd/n42/initcmd.go): decode, hive env overrides, consensus
// normalisation. Tools that re-execute blocks of a chain without a params
// name (the n42-rs devnet, chain id 1143) take their rules from it.
func LoadGenesisChainConfig(path string) (*params.ChainConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	genesis := new(Genesis)
	if err := json.NewDecoder(f).Decode(genesis); err != nil {
		return nil, err
	}
	ApplyHiveGenesisEnv(genesis, os.LookupEnv)
	if genesis.Config == nil {
		return nil, fmt.Errorf("missing chain config")
	}
	return params.NormalizeConsensus(genesis.Config), nil
}
