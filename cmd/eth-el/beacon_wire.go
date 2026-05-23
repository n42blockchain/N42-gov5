// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42el-tagged Caplin service wiring for eth-el.
// Defines caplinHandle which owns the running cl.Service and the
// startCaplin helper that lazily builds an eladapter.Backend over
// an ethel.Node once its DB is ready. Only compiled when the
// n42el build tag is set so the default eth-el build has no cl
// dependency.

//go:build n42el

package main

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/cl"
	"github.com/n42blockchain/N42/internal/cl/eladapter"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/log"
)

// caplinHandle owns the running Caplin service so the shutdown path can
// stop it in reverse order. The wire layer is the only thing in cmd/eth-el
// that imports the cl package — every cl reference lives behind n42el.
type caplinHandle struct {
	svc *cl.Service
}

// startCaplin builds and starts a Caplin service against the given
// eth-el Node. The Backend reads chaindata via node.DB(); we cannot do
// that until node.Start has run, so we register a wrapper service that
// constructs the Backend lazily on first call.
//
// Phase 5–6 left the eladapter Engine API methods stubbed; this wiring
// preserves that contract. Filling them in is tracked in
// internal/cl/eladapter/PHASE6_NOTES.md.
func startCaplin(ctx context.Context, cfg conf.BeaconCfg, node *ethel.Node) (*caplinHandle, error) {
	if cfg.Enabled && node == nil {
		return nil, fmt.Errorf("startCaplin: node must not be nil when caplin is enabled")
	}
	if cfg.Enabled {
		log.Warn("eth-el: Caplin is a Phase 6 stub — sentinel/checkpoint sync/stage loop NOT wired",
			"see", "memory/project_caplin_stub_reality.md",
			"recommendation", "for real-chain sync run an external CL (Lighthouse/Prysm) against engine API at :20014; see docs/ethel/external-cl-runbook.md")
	}

	backend := newEthELBackend(node)
	engine := eladapter.New(backend)

	svc := cl.NewService(ctx, cfg, engine)
	if err := svc.Start(); err != nil {
		return nil, err
	}
	return &caplinHandle{svc: svc}, nil
}

func (h *caplinHandle) Stop() {
	if h == nil || h.svc == nil {
		return
	}
	h.svc.Stop()
}
