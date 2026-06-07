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
// #31 Phase 7 (option B): when BeaconCfg.CheckpointSyncURL is set, Service.Start
// runs the lightweight EL-follower — it drives the EL's Engine API
// forkChoiceUpdated from the finalized beacon checkpoint over HTTP, with the EL
// syncing payloads via its own eth/68 devp2p. The full beacon-gossip stage loop
// (sentinel/p2p/gossip) is intentionally NOT ported (see
// docs/ethel/caplin-merge-plan.md, Phase 7 A-vs-B decision).
func startCaplin(ctx context.Context, cfg conf.BeaconCfg, node *ethel.Node) (*caplinHandle, error) {
	if cfg.Enabled && node == nil {
		return nil, fmt.Errorf("startCaplin: node must not be nil when caplin is enabled")
	}
	if cfg.Enabled && cfg.CheckpointSyncURL == "" {
		log.Warn("eth-el: Caplin enabled but no CheckpointSyncURL — follower idle (no finality source)",
			"recommendation", "set BeaconCfg.CheckpointSyncURL to a beacon checkpoint endpoint, or run an external CL against engine API at :20014 (docs/ethel/external-cl-runbook.md)")
	}

	// Pass node as both providers: ethel.Node implements both
	// chaindbProvider (DB) and executionProvider (RwDB / ChainConfig /
	// Engine / OutFreezer), so the Phase 7.1.1.b real Engine API path
	// can lazy-build the EngineAPIv4 stack on first ExecutePayload.
	backend := newEthELBackendWith(node, node)
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
