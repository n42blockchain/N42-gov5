// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package cl is the N42 fork of Erigon's Caplin consensus-layer
// implementation. The entire subtree is gated behind the `n42el` build tag
// and is linked only into cmd/ethexec, never into the native cmd/n42 path.
package cl

import (
	"context"
	"sync"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/cl/eladapter"
	"github.com/n42blockchain/N42/internal/cl/kvadapter"
	libkv "github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// ExecutionEngine is the seam between Caplin and the N42 EL. The current
// implementation is *eladapter.Adapter, which satisfies the 14-method
// cl/phase1/execution_client.ExecutionEngine interface.
type ExecutionEngine = *eladapter.Adapter

// Service owns the Caplin lifecycle: dedicated MDBX, ExecutionEngine
// adapter, and (in later commits) the phase1 stage loop. It mirrors the
// internal/distributed/messaging/service.go pattern so cmd/ethexec wiring
// is symmetric with other optional N42 services.
type Service struct {
	cfg    conf.BeaconCfg
	engine ExecutionEngine

	db libkv.RwDB // Caplin MDBX, nil while disabled or stopped

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// NewService constructs a Caplin service. It does not start any goroutines,
// open any sockets, or touch the database — call Start to bring it up.
// The given context controls the lifetime of any goroutines a future Start
// implementation will spawn.
func NewService(ctx context.Context, cfg conf.BeaconCfg, engine ExecutionEngine) *Service {
	ctx, cancel := context.WithCancel(ctx)
	return &Service{
		cfg:    cfg,
		engine: engine,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Engine returns the ExecutionEngine adapter so future stage-loop wiring
// can call into the EL via Caplin's canonical 14-method interface.
func (s *Service) Engine() ExecutionEngine { return s.engine }

// DB returns the dedicated Caplin MDBX handle, valid only between Start
// and Stop.
func (s *Service) DB() libkv.RwDB { return s.db }

// Start opens the dedicated Caplin MDBX environment. The phase1 stage loop
// is not yet wired in — see PHASE6_NOTES.md.
func (s *Service) Start() error {
	if !s.cfg.Enabled {
		log.Info("Caplin disabled (BeaconCfg.Enabled=false)")
		return nil
	}

	db, err := kvadapter.Open(s.ctx, kvadapter.Config{DataDir: s.cfg.DataDir})
	if err != nil {
		return err
	}
	s.db = db

	log.Info("Caplin started",
		"network", s.cfg.Network,
		"datadir", s.cfg.DataDir,
		"sentinelPort", s.cfg.SentinelPort,
	)
	return nil
}

// Stop is idempotent and safe to call from a deferred shutdown path.
func (s *Service) Stop() {
	s.once.Do(func() {
		s.cancel()
		if s.db != nil {
			s.db.Close()
			s.db = nil
		}
		if s.cfg.Enabled {
			log.Info("Caplin stopped")
		}
	})
}
