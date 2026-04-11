// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Impl unit for the eth2 package.
// Defines the BlockRewardsCollector and impl types.
// Part of the n42el consensus-layer build.

//go:build n42el

package eth2

import "github.com/n42blockchain/N42/internal/cl/transition/machine"

type Impl = impl

var _ machine.Interface = (*impl)(nil)

type BlockRewardsCollector struct {
	Attestations      uint64
	AttesterSlashings uint64
	ProposerSlashings uint64
	SyncAggregate     uint64
}

type impl struct {
	FullValidation        bool
	BlockRewardsCollector *BlockRewardsCollector
}
