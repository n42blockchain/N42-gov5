// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestNewRouter_DefaultConfig(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}

	r := NewRouter(a, nil)
	if r == nil {
		t.Fatal("NewRouter(nil config) = nil")
	}

	apis := r.APIs()
	if len(apis) == 0 {
		t.Fatal("APIs() returned empty list with default config")
	}

	if r.Metrics() == nil {
		t.Fatal("Metrics() = nil")
	}
}

func TestNewRouter_AllDisabled(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}

	r := NewRouter(a, &RouterConfig{})
	apis := r.APIs()
	if len(apis) != 0 {
		t.Fatalf("APIs() with all namespaces disabled = %d entries, want 0", len(apis))
	}
}

func TestRouter_SetBundlerService_Nil(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}

	r := NewRouter(a, &RouterConfig{})
	r.SetBundlerService(nil)
	apis := r.APIs()
	if len(apis) != 0 {
		t.Fatalf("APIs() with nil bundler = %d entries, want 0", len(apis))
	}
}

func TestRouter_StartMetricsLogger(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}
	r := NewRouter(a, &RouterConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.StartMetricsLogger(ctx, 5*time.Millisecond)
	<-ctx.Done()
}
