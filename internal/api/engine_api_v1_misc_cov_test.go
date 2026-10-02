// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func apiTNewEngineV1(t *testing.T) *EngineAPIV1 {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}
	return NewEngineAPIV1(NewBlockChainAPI(api))
}

func TestEngineAPIV1_HasBlockHash(t *testing.T) {
	e := apiTNewEngineV1(t)
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	h := ethCompatibleBlockHash(genesis, cfg)

	if !e.HasBlockHash(h) {
		t.Fatal("HasBlockHash(genesis) = false, want true")
	}
	if e.HasBlockHash(types.HexToHash("0xdeadbeef")) {
		t.Fatal("HasBlockHash(unknown) = true, want false")
	}
}

func TestEngineAPIV1_ExchangeCapabilities(t *testing.T) {
	e := apiTNewEngineV1(t)
	methods, err := e.ExchangeCapabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExchangeCapabilities() error = %v", err)
	}
	if len(methods) == 0 {
		t.Fatal("ExchangeCapabilities() returned empty list")
	}
}

func TestEngineAPIV1_ExchangeTransitionConfigurationV1(t *testing.T) {
	e := apiTNewEngineV1(t)
	conf := &TransitionConfigurationV1{}
	res, err := e.ExchangeTransitionConfigurationV1(context.Background(), conf)
	if err != nil {
		t.Fatalf("ExchangeTransitionConfigurationV1() error = %v", err)
	}
	if res == nil {
		t.Fatal("ExchangeTransitionConfigurationV1() = nil")
	}
}

func TestSupportedEngineForksAndMethods(t *testing.T) {
	if len(supportedEngineForks()) == 0 {
		t.Fatal("supportedEngineForks() returned empty list")
	}
	if len(supportedEngineMethods()) == 0 {
		t.Fatal("supportedEngineMethods() returned empty list")
	}
}

func TestEngineAPIs(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}
	apis := EngineAPIs(a)
	if len(apis) != 3 {
		t.Fatalf("EngineAPIs() = %d entries, want 3", len(apis))
	}
	for _, d := range apis {
		if d.Namespace != "engine" || !d.Authenticated || d.Service == nil {
			t.Fatalf("EngineAPIs() entry malformed: %+v", d)
		}
	}
}
