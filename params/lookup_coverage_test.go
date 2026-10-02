// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package params

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params/networkname"
)

func TestChainConfigByChainNameAllCases(t *testing.T) {
	names := []string{
		networkname.MainnetChainName,
		networkname.N42MainnetAlias,
		"mainnet_compat",
		"mainnet_v2",
		"mainnet_v2_staggered",
		"mainnet_mpt",
		"mainnet_qmdb",
		"mainnet_qmdb_staggered",
		"qs_epoch_test",
		"h2_interop_test",
		networkname.TestnetChainName,
		networkname.EthereumMainnetChainName,
		networkname.EthereumSepoliaChainName,
		networkname.EthereumTestnetAlias,
		networkname.EthereumHoleskyChainName,
	}
	for _, n := range names {
		if got := ChainConfigByChainName(n); got == nil {
			t.Errorf("ChainConfigByChainName(%q) = nil, want non-nil", n)
		}
	}
	if got := ChainConfigByChainName("totally-unknown-chain"); got != nil {
		t.Errorf("ChainConfigByChainName(unknown) = %v, want nil", got)
	}
}

func TestGenesisHashByChainNameAllCases(t *testing.T) {
	names := []string{
		networkname.MainnetChainName,
		networkname.N42MainnetAlias,
		"mainnet_compat",
		networkname.TestnetChainName,
		networkname.N42TestnetAlias,
		"mainnet_v2",
		"mainnet_mpt",
		"mainnet_v2_staggered",
		"mainnet_qmdb",
		"mainnet_qmdb_staggered",
		"qs_epoch_test",
		"h2_interop_test",
		networkname.EthereumMainnetChainName,
		networkname.EthereumSepoliaChainName,
		networkname.EthereumTestnetAlias,
		networkname.EthereumHoleskyChainName,
	}
	for _, n := range names {
		if got := GenesisHashByChainName(n); got == nil {
			t.Errorf("GenesisHashByChainName(%q) = nil, want non-nil", n)
		}
	}
	if got := GenesisHashByChainName("totally-unknown-chain"); got != nil {
		t.Errorf("GenesisHashByChainName(unknown) = %v, want nil", got)
	}
}

func TestChainConfigByGenesisHashAllCases(t *testing.T) {
	cases := []struct {
		hash types.Hash
		want *ChainConfig
	}{
		{MainnetGenesisHash, MainnetChainConfig},
		{TestnetGenesisHash, TestnetChainConfig},
		{EthereumMainnetGenesisHash, EthereumMainnetChainConfig},
		{EthereumSepoliaGenesisHash, EthereumSepoliaChainConfig},
		{EthereumHoleskyGenesisHash, EthereumHoleskyChainConfig},
	}
	for _, c := range cases {
		if got := ChainConfigByGenesisHash(c.hash); got != c.want {
			t.Errorf("ChainConfigByGenesisHash mismatch for %v", c.hash)
		}
	}
	var zero types.Hash
	if got := ChainConfigByGenesisHash(zero); got != nil {
		t.Errorf("ChainConfigByGenesisHash(zero) = %v, want nil", got)
	}
}

func TestNetworkIDByChainNameAllCases(t *testing.T) {
	names := []string{
		networkname.MainnetChainName,
		networkname.N42MainnetAlias,
		networkname.TestnetChainName,
		networkname.N42TestnetAlias,
		networkname.EthereumMainnetChainName,
		networkname.EthereumSepoliaChainName,
		networkname.EthereumTestnetAlias,
		networkname.EthereumHoleskyChainName,
	}
	for _, n := range names {
		if got := NetworkIDByChainName(n); got == 0 {
			t.Errorf("NetworkIDByChainName(%q) = 0, want non-zero", n)
		}
	}
}

func TestHotStuffConfigString(t *testing.T) {
	cfg := &HotStuffConfig{
		Period:      5,
		BaseTimeout: 1,
		MaxTimeout:  10,
		EpochLength: 100,
		Validators:  []HotStuffValidatorConfig{{}, {}, {}},
	}
	s := cfg.String()
	if !strings.Contains(s, "Validators: 3") {
		t.Fatalf("HotStuffConfig.String() = %q, missing validator count", s)
	}
}
