// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package params

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

func TestEthMainnetGenesisJSON(t *testing.T) {
	data := EthMainnetGenesisJSON()
	if len(data) == 0 {
		t.Fatal("expected non-empty embedded genesis JSON")
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("embedded genesis JSON does not parse: %v", err)
	}
}

func TestNormalizeConsensusInfersFromSubConfigs(t *testing.T) {
	if got := NormalizeConsensus(nil); got != nil {
		t.Fatalf("NormalizeConsensus(nil) = %v, want nil", got)
	}

	// Explicit consensus is left untouched.
	explicit := &ChainConfig{Consensus: CliqueConsensus}
	if got := NormalizeConsensus(explicit); got.Consensus != CliqueConsensus {
		t.Fatalf("explicit consensus overwritten: %v", got.Consensus)
	}

	cases := []struct {
		name string
		cfg  *ChainConfig
		want ConsensusType
	}{
		{"clique", &ChainConfig{Clique: &CliqueConfig{}}, CliqueConsensus},
		{"apos", &ChainConfig{Apos: &APosConfig{}}, AposConsensu},
		{"hotstuff", &ChainConfig{HotStuff: &HotStuffConfig{}}, HotStuffConsensus},
		{"aura", &ChainConfig{Aura: &AuRaConfig{}}, AuRaConsensus},
		{"parlia", &ChainConfig{Parlia: &ParliaConfig{}}, ParliaConsensus},
		{"bor", &ChainConfig{Bor: &BorConfig{}}, BorConsensus},
		{"ethash", &ChainConfig{Ethash: &EthashConfig{}}, EtHashConsensus},
		{"none", &ChainConfig{}, Faker},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeConsensus(c.cfg)
			if got.Consensus != c.want {
				t.Errorf("NormalizeConsensus(%s) = %v, want %v", c.name, got.Consensus, c.want)
			}
		})
	}
}

func TestEthereumReceiptEncoding(t *testing.T) {
	if (*ChainConfig)(nil).EthereumReceiptEncoding() {
		t.Fatal("nil ChainConfig should report false")
	}

	mpt := &ChainConfig{StateScheme: string(StateCommitmentPresetEthereumMPT)}
	if !mpt.EthereumReceiptEncoding() {
		t.Fatal("ethereum MPT state scheme should report true")
	}

	hsCompat := &ChainConfig{HotStuff: &HotStuffConfig{EthELCompat: true}}
	if !hsCompat.EthereumReceiptEncoding() {
		t.Fatal("hotstuff EthELCompat should report true")
	}

	hsNoCompat := &ChainConfig{HotStuff: &HotStuffConfig{EthELCompat: false}}
	if hsNoCompat.EthereumReceiptEncoding() {
		t.Fatal("hotstuff without EthELCompat should report false")
	}

	plain := &ChainConfig{}
	if plain.EthereumReceiptEncoding() {
		t.Fatal("default config should report false")
	}
}

func TestChainConfigStringContents(t *testing.T) {
	cfg := &ChainConfig{}
	s := cfg.String()
	if !strings.Contains(s, "ChainID") {
		t.Fatalf("String() should mention ChainID: %q", s)
	}
}

func TestDAODrainList(t *testing.T) {
	list := DAODrainList()
	if len(list) == 0 {
		t.Fatal("DAODrainList should be non-empty")
	}
	seen := map[string]bool{}
	for _, addr := range list {
		s := addr.String()
		if seen[s] {
			t.Fatalf("duplicate address in DAODrainList: %s", s)
		}
		seen[s] = true
	}
}

func TestProfileFamily(t *testing.T) {
	cases := []struct {
		p    ProfileDescriptor
		fam  ProfileFamily
		isEL bool
		isN  bool
	}{
		{ProfileDescriptor{name: ExecutionProfileEthereumEL, family: ProfileFamilyEthereumEL}, ProfileFamilyEthereumEL, true, false},
		{ProfileDescriptor{name: ExecutionProfileN42, family: ProfileFamilyN42}, ProfileFamilyN42, false, true},
	}
	for _, c := range cases {
		if got := c.p.Family(); got != c.fam {
			t.Errorf("Family() = %v, want %v", got, c.fam)
		}
		if got := c.p.IsEthereumEL(); got != c.isEL {
			t.Errorf("IsEthereumEL() = %v, want %v", got, c.isEL)
		}
		if got := c.p.IsN42(); got != c.isN {
			t.Errorf("IsN42() = %v, want %v", got, c.isN)
		}
		if got := c.p.String(); got != string(c.p.name) {
			t.Errorf("String() = %v, want %v", got, c.p.name)
		}
		if got := c.p.Name(); got != c.p.name {
			t.Errorf("Name() = %v, want %v", got, c.p.name)
		}
	}
}

func TestApplyBinanceSmartChainParams(t *testing.T) {
	saved := GasLimitBoundDivisor
	defer func() { GasLimitBoundDivisor = saved }()

	GasLimitBoundDivisor = 1
	ApplyBinanceSmartChainParams()
	if GasLimitBoundDivisor != 256 {
		t.Fatalf("GasLimitBoundDivisor = %d, want 256", GasLimitBoundDivisor)
	}
}

func TestIsLtHashIsTxRootBlake3IsDeferredExecution(t *testing.T) {
	cfg := &ChainConfig{
		LtHashTime:            big.NewInt(100),
		TxRootBlake3Time:      big.NewInt(200),
		DeferredExecutionTime: big.NewInt(300),
	}

	if cfg.IsLtHash(50) {
		t.Error("IsLtHash(50) should be false before fork time")
	}
	if !cfg.IsLtHash(100) {
		t.Error("IsLtHash(100) should be true at fork time")
	}
	if !cfg.IsLtHash(150) {
		t.Error("IsLtHash(150) should be true after fork time")
	}

	if cfg.IsTxRootBlake3(199) {
		t.Error("IsTxRootBlake3(199) should be false before fork time")
	}
	if !cfg.IsTxRootBlake3(200) {
		t.Error("IsTxRootBlake3(200) should be true at fork time")
	}

	if cfg.IsDeferredExecution(299) {
		t.Error("IsDeferredExecution(299) should be false before fork time")
	}
	if !cfg.IsDeferredExecution(300) {
		t.Error("IsDeferredExecution(300) should be true at fork time")
	}

	// nil fork times never activate.
	bare := &ChainConfig{}
	if bare.IsLtHash(1<<62) || bare.IsTxRootBlake3(1<<62) || bare.IsDeferredExecution(1 << 62) {
		t.Error("nil fork times should never report forked")
	}
}

