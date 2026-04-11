// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/utils/threading"
)

func ProcessSlashings(s abstract.BeaconState) error {
	defer monitor.ObserveElaspedTime(monitor.ProcessSlashingsTime).End()
	if s.Version().AfterOrEqual(clparams.ElectraVersion) {
		// Switch to Electra slashing
		return processSlashingsElectra(s)
	}

	epoch := state.Epoch(s)
	// Get the total active balance
	totalBalance := s.GetTotalActiveBalance()
	// Calculate the total slashing amount
	// by summing all slashings and multiplying by the provided multiplier
	// Adjust the total slashing amount to be no greater than the total active balance
	slashing := min(totalBalance, state.GetTotalSlashingAmount(s)*s.BeaconConfig().GetProportionalSlashingMultiplier(s.Version()))
	beaconConfig := s.BeaconConfig()
	// Apply penalties to validators who have been slashed and reached the withdrawable epoch
	return threading.ParallellForLoop(1, 0, s.ValidatorSet().Length(), func(i int) error {
		validator := s.ValidatorSet().Get(i)
		if !validator.Slashed() || epoch+beaconConfig.EpochsPerSlashingsVector/2 != validator.WithdrawableEpoch() {
			return nil
		}
		// Get the effective balance increment
		increment := beaconConfig.EffectiveBalanceIncrement
		// Calculate the penalty numerator by multiplying the validator's effective balance by the total slashing amount
		penaltyNumerator := validator.EffectiveBalance() / increment * slashing
		// Calculate the penalty by dividing the penalty numerator by the total balance and multiplying by the increment
		penalty := penaltyNumerator / totalBalance * increment
		// Decrease the validator's balance by the calculated penalty
		return state.DecreaseBalance(s, uint64(i), penalty)
	})
}

func processSlashingsElectra(s abstract.BeaconState) error {
	// see: https://github.com/ethereum/consensus-specs/blob/dev/specs/electra/beacon-chain.md#modified-process_slashings
	epoch := state.Epoch(s)
	totalBalance := s.GetTotalActiveBalance()
	adjustTotalSlashingBalance := min(
		state.GetTotalSlashingAmount(s)*s.BeaconConfig().GetProportionalSlashingMultiplier(s.Version()),
		totalBalance,
	)
	cfg := s.BeaconConfig()
	increment := cfg.EffectiveBalanceIncrement
	penaltyPerEffectiveBalanceIncr := adjustTotalSlashingBalance / (totalBalance / increment)
	return threading.ParallellForLoop(1, 0, s.ValidatorSet().Length(), func(i int) error {
		v := s.ValidatorSet().Get(i)
		if !v.Slashed() || epoch+cfg.EpochsPerSlashingsVector/2 != v.WithdrawableEpoch() {
			return nil
		}
		effectiveBalanceIncrements := v.EffectiveBalance() / increment
		penalty := penaltyPerEffectiveBalanceIncr * effectiveBalanceIncrements
		return state.DecreaseBalance(s, uint64(i), penalty)
	})
}
