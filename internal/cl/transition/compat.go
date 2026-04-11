// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package transition

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/transition/impl/eth2"
	machine2 "github.com/n42blockchain/N42/internal/cl/transition/machine"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
)

var _ machine2.Interface = (*eth2.Impl)(nil)

var DefaultMachine = &eth2.Impl{}
var ValidatingMachine = &eth2.Impl{FullValidation: true}

func TransitionState(s abstract.BeaconState, block *cltypes.SignedBeaconBlock, blockRewardsCollector *eth2.BlockRewardsCollector, fullValidation bool) error {
	cvm := &eth2.Impl{FullValidation: fullValidation, BlockRewardsCollector: blockRewardsCollector}
	return machine2.TransitionState(cvm, s, block)
}
