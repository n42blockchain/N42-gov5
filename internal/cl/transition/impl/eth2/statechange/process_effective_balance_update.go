// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Process effective balance update unit for the statechange package.
// Exports helpers such as ProcessEffectiveBalanceUpdates.
// Per-slot and per-epoch state change routines.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
)

// ProcessEffectiveBalanceUpdates updates the effective balance of validators. Specs at: https://github.com/ethereum/consensus-specs/blob/dev/specs/phase0/beacon-chain.md#effective-balances-updates
func ProcessEffectiveBalanceUpdates(s abstract.BeaconState) error {
	defer monitor.ObserveElaspedTime(monitor.ProcessEffectiveBalanceUpdatesTime).End()
	beaconConfig := s.BeaconConfig()
	// Define non-changing constants to avoid recomputation.
	histeresisIncrement := beaconConfig.EffectiveBalanceIncrement / beaconConfig.HysteresisQuotient
	downwardThreshold := histeresisIncrement * beaconConfig.HysteresisDownwardMultiplier
	upwardThreshold := histeresisIncrement * beaconConfig.HysteresisUpwardMultiplier

	// Iterate over validator set and compute the diff of each validator.
	var err error
	var balance uint64
	s.ForEachValidator(func(validator solid.Validator, index, total int) bool {
		balance, err = s.ValidatorBalance(index)
		if err != nil {
			return false
		}
		eb := validator.EffectiveBalance()
		if balance+downwardThreshold < eb || eb+upwardThreshold < balance {
			// Set new effective balance
			maxEffectiveBalance := state.GetMaxEffectiveBalanceByVersion(validator, s.BeaconConfig(), s.Version())
			effectiveBalance := min(balance-(balance%beaconConfig.EffectiveBalanceIncrement), maxEffectiveBalance)
			s.SetEffectiveBalanceForValidatorAtIndex(index, effectiveBalance)
		}
		return true
	})
	if err != nil {
		return err
	}
	return nil
}
