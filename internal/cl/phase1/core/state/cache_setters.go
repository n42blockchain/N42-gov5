// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cache setters unit for the state package.
// Exports helpers such as SetSlot and AddValidator.
// Part of the n42el consensus-layer build.

//go:build n42el

package state

import "github.com/n42blockchain/N42/internal/cl/cltypes/solid"

// Below are setters.

func (b *CachingBeaconState) SetSlot(slot uint64) {
	b.BeaconState.SetSlot(slot)
	b.proposerIndex = nil
	if slot%b.BeaconConfig().SlotsPerEpoch == 0 {
		b.totalActiveBalanceCache = nil
	}
}

func (b *CachingBeaconState) AddValidator(validator solid.Validator, balance uint64) {
	b.BeaconState.AddValidator(validator, balance)
	pk := validator.PublicKey()
	b.publicKeyIndicies.Set(pk[:], uint64(b.ValidatorLength())-1)
	// change in validator set means cache purging
	b.totalActiveBalanceCache = nil
}
