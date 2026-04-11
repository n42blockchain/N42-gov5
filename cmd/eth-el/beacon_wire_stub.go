// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !n42el

package main

import (
	"context"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
)

// caplinHandle is a no-op placeholder used when the n42el build tag is NOT
// set. The default eth-el build links none of the cl/ subtree.
type caplinHandle struct{}

func startCaplin(_ context.Context, _ conf.BeaconCfg, _ *ethel.Node) (*caplinHandle, error) {
	return &caplinHandle{}, nil
}

func (h *caplinHandle) Stop() {}
