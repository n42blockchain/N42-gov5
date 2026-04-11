// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package bootstrap implements the one-shot "leaves-journal → PlainState"
// initialisation phase of eth-el.
//
// The package is a subpackage of internal/ethel (not a file inside it)
// for two reasons:
//
//  1. It imports internal/ethel for RebuildState, which would be a cycle
//     if the service lived in the parent package (internal/ethel already
//     has types that internal/api imports transitively).
//  2. It is cmd/eth-el-only and should stay out of the internal/ethel
//     surface so other binaries don't accidentally depend on the eth-el
//     bootstrap path.
//
// Lifecycle:
//
//	Start:
//	  1. if disabled or chaindata already populated → return nil
//	  2. if cfg.Manifest == "" → call RebuildState on the existing local
//	     leaves freezer (manual-deployment mode)
//	  3. otherwise: LoadManifest → for each Asset, Fetcher.Fetch into the
//	     leaves dir → call RebuildState
//
//	Stop:  no-op (the service is one-shot; the fetcher + node own any
//	       long-lived resources)
package bootstrap

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// stateRebuilder is the narrow surface the bootstrap service needs from
// internal/ethel.RebuildState. Having it as an interface lets the unit
// tests substitute a recording fake without touching the real MDBX/state
// rebuild machinery.
type stateRebuilder func(ctx context.Context, db kv.RwDB, leavesDir string, endBlock uint64) error

// nodeAccessor is the narrow surface the service needs from an
// *ethel.Node. Defining it locally (instead of referencing *ethel.Node
// directly) removes a type dependency and, more importantly, lets tests
// pass a stub Node without setting up storage.
type nodeAccessor interface {
	RwDB() kv.RwDB
	LeavesDir() string
	HasPopulatedState(ctx context.Context) (bool, error)
}

// Service is the eth-el bootstrap service. It implements ethel.Service
// so it can be registered through Node.RegisterFactory.
type Service struct {
	cfg     conf.BootstrapCfg
	node    nodeAccessor
	fetcher fetch.Fetcher

	// rebuild is the pluggable state-rebuild entry point; set to
	// ethel.RebuildState in production and a fake in tests.
	rebuild stateRebuilder
}

// New constructs a production Bootstrap Service. fetcher may be nil if
// manifest-driven bootstrap is not configured — in that case the
// service runs in "manual deployment" mode and only invokes RebuildState
// on whatever is already on disk.
func New(cfg conf.BootstrapCfg, node *ethel.Node, fetcher fetch.Fetcher) *Service {
	return &Service{
		cfg:     cfg,
		node:    node,
		fetcher: fetcher,
		rebuild: ethel.RebuildState,
	}
}

// newForTest is the test-only constructor that accepts a stubbed node
// and rebuild function. Keeping it out of the public API prevents
// accidental misuse in production wiring.
func newForTest(cfg conf.BootstrapCfg, node nodeAccessor, fetcher fetch.Fetcher, rebuild stateRebuilder) *Service {
	return &Service{cfg: cfg, node: node, fetcher: fetcher, rebuild: rebuild}
}

func (s *Service) Name() string { return "bootstrap" }

// Start runs the entire bootstrap pipeline synchronously. The caller is
// an ethel.Node service-loop goroutine; a non-nil return aborts the
// whole node startup.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		log.Info("eth-el: bootstrap disabled by config")
		return nil
	}
	if s.node == nil {
		return fmt.Errorf("bootstrap: nil node")
	}

	already, err := s.node.HasPopulatedState(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap: probe chaindata: %w", err)
	}
	if already {
		log.Info("eth-el: bootstrap skipped (chaindata already populated)")
		return nil
	}

	leavesDir := s.node.LeavesDir()

	if s.cfg.Manifest != "" {
		if err := s.runManifest(ctx, leavesDir); err != nil {
			return fmt.Errorf("bootstrap: manifest fetch: %w", err)
		}
	} else {
		log.Info("eth-el: no bootstrap manifest configured, using local leaves",
			"dir", leavesDir)
	}

	log.Info("eth-el: rebuilding state from leaves journal",
		"dir", leavesDir, "endBlock", s.cfg.EndBlock)
	if err := s.rebuild(ctx, s.node.RwDB(), leavesDir, s.cfg.EndBlock); err != nil {
		return fmt.Errorf("bootstrap: RebuildState: %w", err)
	}
	log.Info("eth-el: bootstrap complete")
	return nil
}

// Stop is a no-op — bootstrap is one-shot, all work is done in Start.
func (s *Service) Stop() error { return nil }

// runManifest loads the configured manifest, validates it is a leaves
// manifest, and fetches every declared Asset into leavesDir.
func (s *Service) runManifest(ctx context.Context, leavesDir string) error {
	if s.fetcher == nil {
		return fmt.Errorf("bootstrap: manifest %q configured but no Fetcher available", s.cfg.Manifest)
	}

	log.Info("eth-el: loading bootstrap manifest", "url", s.cfg.Manifest)
	manifest, err := fetch.LoadManifest(ctx, s.cfg.Manifest)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if manifest.Kind != fetch.ManifestKindLeaves {
		return fmt.Errorf("manifest kind is %q, expected %q", manifest.Kind, fetch.ManifestKindLeaves)
	}

	assets := manifest.AssetList()
	log.Info("eth-el: bootstrap manifest loaded",
		"assets", len(assets),
		"chain_id", manifest.ChainID,
		"updated_at", manifest.UpdatedAt)

	progress := newProgressLogger()
	for i, asset := range assets {
		log.Info("eth-el: fetching leaves asset",
			"index", i+1, "total", len(assets),
			"name", asset.Name, "size_mb", asset.SizeBytes/(1<<20))
		if err := s.fetcher.Fetch(ctx, asset, leavesDir, progress.callback(asset.Name)); err != nil {
			return fmt.Errorf("asset %s: %w", asset.Name, err)
		}
	}
	log.Info("eth-el: bootstrap manifest fetch complete",
		"assets", len(assets), "dir", leavesDir)
	return nil
}
