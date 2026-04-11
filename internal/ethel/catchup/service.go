// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package catchup is the one-shot "download segments + replay to tip"
// phase of eth-el. It bridges bootstrap (which produces a PlainState at
// some recent block N) and the engineAPI live loop (which expects the
// chaindata head to already be close to the real chain tip so the CL
// can drive NewPayload calls forward).
//
// The service is a subpackage of internal/ethel — not a file inside it —
// for the same layering reason bootstrap and engineapi are: it calls
// through to internal/ethel.CatchUp, and keeping the dependency edge
// pointing inward keeps the package graph acyclic.
//
// Lifecycle:
//
//	Start:
//	  1. if disabled → return nil
//	  2. if cfg.Manifest != "" && fetcher != nil → LoadManifest →
//	     fetch every segment into the freezer dir
//	  3. runCatchUp(ctx) → the existing ethel.CatchUp, which reads from
//	     the freezer and runs the executor up to the highest available
//	     block. It is a no-op when chaindata is already at that height.
//
//	Stop: no-op (one-shot)
package catchup

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// nodeAccessor is the narrow surface the service needs from an
// *ethel.Node. Defined locally so unit tests can stub it without
// opening a real MDBX environment.
type nodeAccessor interface {
	RwDB() kv.RwDB
	LeavesDir() string
	InputFreezer() *freezer.Freezer
	OutFreezer() *freezer.Freezer
	ChainConfig() *params.ChainConfig
	Engine() consensus.Engine
}

// catchUpRunner is the pluggable entry point for the executor replay
// step. Production wiring sets it to ethel.CatchUp; tests substitute a
// recording fake.
type catchUpRunner func(ctx context.Context, cfg ethel.CatchUpConfig) error

// Service implements ethel.Service. It runs the segment-fetch phase
// first (if a manifest is configured), then the executor replay phase.
type Service struct {
	cfg      conf.CatchUpCfg
	manifest string
	node     nodeAccessor
	fetcher  fetch.Fetcher
	catchUp  catchUpRunner
}

// Config bundles every catchup-specific knob. Kept small on purpose —
// most tuning lives on conf.CatchUpCfg.
type Config struct {
	CatchUp conf.CatchUpCfg

	// Manifest is the URL or file path of a segments manifest. Empty
	// means "no fetch phase, just run the executor on whatever is
	// already in the freezer".
	Manifest string
}

// New constructs a production catchup Service. fetcher may be nil if
// no segments manifest is configured; the service then degrades to
// "executor only" mode.
func New(cfg Config, node *ethel.Node, fetcher fetch.Fetcher) *Service {
	return &Service{
		cfg:      cfg.CatchUp,
		manifest: cfg.Manifest,
		node:     node,
		fetcher:  fetcher,
		catchUp:  ethel.CatchUp,
	}
}

// newForTest is the test-only constructor that accepts a stubbed node
// and catch-up runner. Not exported.
func newForTest(cfg Config, node nodeAccessor, fetcher fetch.Fetcher, runner catchUpRunner) *Service {
	return &Service{
		cfg:      cfg.CatchUp,
		manifest: cfg.Manifest,
		node:     node,
		fetcher:  fetcher,
		catchUp:  runner,
	}
}

func (s *Service) Name() string { return "catch-up" }

// Start executes the full catch-up pipeline synchronously.
func (s *Service) Start(ctx context.Context) error {
	if s.node == nil {
		return fmt.Errorf("catch-up: nil node")
	}

	if s.manifest != "" {
		if err := s.runManifest(ctx); err != nil {
			return fmt.Errorf("catch-up: manifest fetch: %w", err)
		}
	}

	log.Info("eth-el: running catch-up executor",
		"commitInterval", s.cfg.CommitInterval,
		"verifyInterval", s.cfg.VerifyInterval)
	cfg := ethel.CatchUpConfig{
		InputFreezer:   s.node.InputFreezer(),
		OutputFreezer:  s.node.OutFreezer(),
		DB:             s.node.RwDB(),
		ChainConfig:    s.node.ChainConfig(),
		Engine:         s.node.Engine(),
		CommitInterval: s.cfg.CommitInterval,
	}
	if err := s.catchUp(ctx, cfg); err != nil {
		return fmt.Errorf("catch-up: executor: %w", err)
	}
	log.Info("eth-el: catch-up complete")
	return nil
}

// Stop is a no-op — one-shot service.
func (s *Service) Stop() error { return nil }

// runManifest loads the segments manifest, validates its kind, and
// fetches every declared Asset into the freezer dir. The freezer dir
// shared with the output freezer is deliberate: segments should land
// alongside any locally-built data so the single Node.inputFreezer
// handle picks them up automatically.
func (s *Service) runManifest(ctx context.Context) error {
	if s.fetcher == nil {
		return fmt.Errorf("catch-up: manifest %q configured but no Fetcher available", s.manifest)
	}
	freezerDir := s.node.LeavesDir() // = <datadir>/chain/freezer

	log.Info("eth-el: loading catch-up manifest", "url", s.manifest)
	manifest, err := fetch.LoadManifest(ctx, s.manifest)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if manifest.Kind != fetch.ManifestKindSegments {
		return fmt.Errorf("manifest kind is %q, expected %q", manifest.Kind, fetch.ManifestKindSegments)
	}

	assets := manifest.AssetList()
	log.Info("eth-el: catch-up manifest loaded",
		"assets", len(assets),
		"chain_id", manifest.ChainID,
		"updated_at", manifest.UpdatedAt)

	progress := newProgressLogger()
	for i, asset := range assets {
		log.Info("eth-el: fetching segment asset",
			"index", i+1, "total", len(assets),
			"name", asset.Name, "size_mb", asset.SizeBytes/(1<<20))
		if err := s.fetcher.Fetch(ctx, asset, freezerDir, progress.callback(asset.Name)); err != nil {
			return fmt.Errorf("asset %s: %w", asset.Name, err)
		}
	}
	log.Info("eth-el: catch-up manifest fetch complete",
		"assets", len(assets), "dir", freezerDir)
	return nil
}
