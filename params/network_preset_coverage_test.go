// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package params

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params/networkname"
)

func TestResolveNetworkPresetAllChains(t *testing.T) {
	n42Chains := []string{
		networkname.MainnetChainName,
		networkname.N42MainnetAlias,
		networkname.TestnetChainName,
		networkname.N42TestnetAlias,
		"mainnet_compat",
		"mainnet_v2",
		"mainnet_qmdb",
		"mainnet_qmdb_staggered",
		"qs_epoch_test",
		"h2_interop_test",
	}
	for _, c := range n42Chains {
		preset, err := ResolveNetworkPreset(c, "")
		if err != nil {
			t.Errorf("ResolveNetworkPreset(%q, \"\") error: %v", c, err)
		}
		if !preset.Profile.IsN42() {
			t.Errorf("ResolveNetworkPreset(%q) profile = %v, want n42", c, preset.Profile)
		}
	}

	elChains := []string{
		networkname.EthereumMainnetChainName,
		networkname.EthereumSepoliaChainName,
		networkname.EthereumTestnetAlias,
		networkname.EthereumHoleskyChainName,
	}
	for _, c := range elChains {
		preset, err := ResolveNetworkPreset(c, "")
		if err != nil {
			t.Errorf("ResolveNetworkPreset(%q, \"\") error: %v", c, err)
		}
		if !preset.Profile.IsEthereumEL() {
			t.Errorf("ResolveNetworkPreset(%q) profile = %v, want eth-el", c, preset.Profile)
		}
		if !preset.RequiresExplicitInit {
			t.Errorf("ResolveNetworkPreset(%q) should require explicit init", c)
		}
	}

	// Default chain (empty string -> mainnet).
	if preset, err := ResolveNetworkPreset("", ""); err != nil || preset.Chain != networkname.MainnetChainName {
		t.Errorf("ResolveNetworkPreset(\"\",\"\") = %+v, %v", preset, err)
	}

	// Case-insensitive, whitespace trimmed.
	if preset, err := ResolveNetworkPreset("  MAINNET  ", ""); err != nil || preset.Chain != networkname.MainnetChainName {
		t.Errorf("ResolveNetworkPreset with whitespace/case = %+v, %v", preset, err)
	}

	// Unknown chain.
	if _, err := ResolveNetworkPreset("not-a-real-chain", ""); err == nil {
		t.Error("expected error for unknown chain")
	}

	// Mismatched explicit profile hint on a chain that requires a specific profile.
	if _, err := ResolveNetworkPreset(networkname.MainnetChainName, string(ExecutionProfileEthereumEL)); err == nil {
		t.Error("expected error for mismatched profile hint")
	}

	// Invalid profile hint string entirely.
	if _, err := ResolveNetworkPreset(networkname.MainnetChainName, "bogus-profile"); err == nil {
		t.Error("expected error for invalid profile hint")
	}

	// private chain, default profile.
	if preset, err := ResolveNetworkPreset("private", ""); err != nil || preset.Commitment != StateCommitmentPresetQMDB {
		t.Errorf("ResolveNetworkPreset(private, \"\") = %+v, %v, want QMDB commitment", preset, err)
	}

	// private chain, eth-el profile -> ethereum MPT commitment.
	if preset, err := ResolveNetworkPreset("private", string(ExecutionProfileEthereumEL)); err != nil || preset.Commitment != StateCommitmentPresetEthereumMPT {
		t.Errorf("ResolveNetworkPreset(private, eth-el) = %+v, %v, want MPT commitment", preset, err)
	}

	// private chain with an invalid profile hint.
	if _, err := ResolveNetworkPreset("private", "bogus"); err == nil {
		t.Error("expected error for invalid profile hint on private chain")
	}
}

func TestInferNetworkPresetFromChainConfig(t *testing.T) {
	if _, ok := InferNetworkPresetFromChainConfig(nil); ok {
		t.Error("nil config should not resolve")
	}
	if _, ok := InferNetworkPresetFromChainConfig(&ChainConfig{}); ok {
		t.Error("config with nil ChainID should not resolve")
	}

	mkCfg := func(id int64) *ChainConfig {
		return &ChainConfig{ChainID: big.NewInt(id)}
	}

	// chainId 94 variants.
	qmdb := mkCfg(94)
	qmdb.StateScheme = string(StateCommitmentPresetQMDB)
	if preset, ok := InferNetworkPresetFromChainConfig(qmdb); !ok || preset.Chain != "mainnet_qmdb" {
		t.Errorf("qmdb variant: %+v, %v", preset, ok)
	}

	qmdbStaggered := mkCfg(94)
	qmdbStaggered.StateScheme = string(StateCommitmentPresetQMDB)
	qmdbStaggered.ShanghaiBlock = big.NewInt(5)
	if preset, ok := InferNetworkPresetFromChainConfig(qmdbStaggered); !ok || preset.Chain != "mainnet_qmdb_staggered" {
		t.Errorf("qmdb_staggered variant: %+v, %v", preset, ok)
	}

	compat := mkCfg(94)
	if preset, ok := InferNetworkPresetFromChainConfig(compat); !ok || preset.Chain != "mainnet_compat" {
		t.Errorf("mainnet_compat variant: %+v, %v", preset, ok)
	}

	v2 := mkCfg(94)
	v2.ShanghaiBlock = big.NewInt(0)
	v2.CancunBlock = big.NewInt(0)
	v2.BeijingBlock = big.NewInt(0)
	if preset, ok := InferNetworkPresetFromChainConfig(v2); !ok || preset.Chain != "mainnet_v2" {
		t.Errorf("mainnet_v2 variant: %+v, %v", preset, ok)
	}

	// Fallback within chainId 94 when none of the sub-conditions match
	// (ShanghaiBlock set but non-zero, CancunBlock nil): defaults to mainnet.
	fallback94 := mkCfg(94)
	fallback94.ShanghaiBlock = big.NewInt(7)
	if preset, ok := InferNetworkPresetFromChainConfig(fallback94); !ok || preset.Chain != networkname.MainnetChainName {
		t.Errorf("fallback 94 variant: %+v, %v", preset, ok)
	}

	if preset, ok := InferNetworkPresetFromChainConfig(mkCfg(95)); !ok || preset.Chain != "qs_epoch_test" {
		t.Errorf("chainId 95: %+v, %v", preset, ok)
	}
	if preset, ok := InferNetworkPresetFromChainConfig(mkCfg(1142)); !ok || preset.Chain != networkname.TestnetChainName {
		t.Errorf("chainId 1142: %+v, %v", preset, ok)
	}
	if preset, ok := InferNetworkPresetFromChainConfig(mkCfg(1)); !ok || preset.Chain != networkname.EthereumMainnetChainName {
		t.Errorf("chainId 1: %+v, %v", preset, ok)
	}
	if preset, ok := InferNetworkPresetFromChainConfig(mkCfg(11155111)); !ok || preset.Chain != networkname.EthereumSepoliaChainName {
		t.Errorf("chainId 11155111: %+v, %v", preset, ok)
	}

	// Unrecognized chainId falls through to consensus-based inference.
	hs := mkCfg(999999)
	hs.Consensus = HotStuffConsensus
	if preset, ok := InferNetworkPresetFromChainConfig(hs); !ok || preset.Chain != "private" || !preset.Profile.IsN42() {
		t.Errorf("hotstuff fallback: %+v, %v", preset, ok)
	}

	apos := mkCfg(999999)
	apos.Consensus = AposConsensu
	if _, ok := InferNetworkPresetFromChainConfig(apos); !ok {
		t.Error("apos fallback should resolve")
	}

	clique := mkCfg(999999)
	clique.Consensus = CliqueConsensus
	if preset, ok := InferNetworkPresetFromChainConfig(clique); !ok || preset.Chain != "private" || !preset.Profile.IsEthereumEL() {
		t.Errorf("clique fallback: %+v, %v", preset, ok)
	}

	faker := mkCfg(999999)
	faker.Consensus = Faker
	if _, ok := InferNetworkPresetFromChainConfig(faker); !ok {
		t.Error("faker fallback should resolve")
	}

	ethash := mkCfg(999999)
	ethash.Consensus = EtHashConsensus
	if _, ok := InferNetworkPresetFromChainConfig(ethash); !ok {
		t.Error("ethash fallback should resolve")
	}

	unknown := mkCfg(999999)
	unknown.Consensus = "totally-unknown-consensus"
	if _, ok := InferNetworkPresetFromChainConfig(unknown); ok {
		t.Error("unknown consensus should not resolve")
	}
}

func TestApplyStateCommitmentPreset(t *testing.T) {
	ApplyStateCommitmentPreset(nil, StateCommitmentPresetQMDB) // must not panic

	cfg := &ChainConfig{}
	ApplyStateCommitmentPreset(cfg, StateCommitmentPresetBMT)
	if cfg.StateScheme != string(StateCommitmentPresetBMT) {
		t.Fatalf("StateScheme = %q, want %q", cfg.StateScheme, StateCommitmentPresetBMT)
	}

	// Already-set scheme is left untouched.
	cfg2 := &ChainConfig{StateScheme: "custom"}
	ApplyStateCommitmentPreset(cfg2, StateCommitmentPresetVerkle)
	if cfg2.StateScheme != "custom" {
		t.Fatalf("StateScheme overwritten: %q", cfg2.StateScheme)
	}
}

func TestStateCommitmentPresetStateScheme(t *testing.T) {
	if StateCommitmentPresetJMT.StateScheme() != "jmt-blake3" {
		t.Fatalf("unexpected StateScheme(): %q", StateCommitmentPresetJMT.StateScheme())
	}
}
