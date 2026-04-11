// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Helpers unit for the machine package.
// Part of the n42el consensus-layer build.
// Part of the n42el consensus-layer build.

//go:build n42el

package machine

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

func executionEnabled(s abstract.BeaconState, blockHash common.Hash) bool {
	return (!state.IsMergeTransitionComplete(s) && blockHash != common.Hash{}) || state.IsMergeTransitionComplete(s)
}
