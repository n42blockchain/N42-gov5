// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !n42el

package eldevp2p

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Config mirrors the n42el implementation so cmd/eth-el can compile in the
// default build while keeping the optional devp2p implementation out of the
// binary.
type Config struct {
	Enabled         bool
	ListenAddr      string
	MaxPeers        int
	BootNodes       []string
	HashedCanonical bool
	// SnapshotCold mirrors service.go's snapshot-direct cold reader; unused
	// by the stub but required so cmd/eth-el compiles without -tags n42el.
	SnapshotCold state.StateReader
	// FreezerDir likewise mirrors service.go. Every field cmd/eth-el assigns
	// MUST exist here too: the default build compiles this file instead of
	// service.go, so a field added on one side only breaks `go build ./...`
	// for everyone not passing -tags n42el.
	FreezerDir              string
	EnodeFile               string
	InvalidAncestorObserver func(rejectedHead, latestValidHash types.Hash)
}

func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		ListenAddr: ":30303",
		MaxPeers:   50,
		BootNodes:  params.EthereumMainnetBootnodes,
	}
}

type Service struct {
	cfg Config
}

func New(cfg Config, _ any, _ types.Hash, _ uint64) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Name() string { return "el-devp2p" }

func (s *Service) Start(context.Context) error {
	if s.cfg.Enabled {
		return errors.New("eldevp2p requires building with -tags n42el")
	}
	return nil
}

func (s *Service) Stop() error { return nil }
