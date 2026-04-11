// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/utils/threading"
)

// ProcessInactivityScores will updates the inactivity registry of each validator.
func ProcessInactivityScores(s abstract.BeaconState, eligibleValidatorsIndicies []uint64, unslashedIndicies [][]bool) error {
	defer monitor.ObserveElaspedTime(monitor.ProcessInactivityScoresTime).End()
	if state.Epoch(s) == s.BeaconConfig().GenesisEpoch {
		return nil
	}

	return threading.ParallellForLoop(1, 0, len(eligibleValidatorsIndicies), func(i int) error {
		validatorIndex := eligibleValidatorsIndicies[i]

		// retrieve validator inactivity score index.
		score, err := s.ValidatorInactivityScore(int(validatorIndex))
		if err != nil {
			return err
		}
		if score == 0 && unslashedIndicies[s.BeaconConfig().TimelyTargetFlagIndex][validatorIndex] {
			return nil
		}

		if unslashedIndicies[s.BeaconConfig().TimelyTargetFlagIndex][validatorIndex] {
			score -= min(1, score)
		} else {
			score += s.BeaconConfig().InactivityScoreBias
		}
		if !state.InactivityLeaking(s) {
			score -= min(s.BeaconConfig().InactivityScoreRecoveryRate, score)
		}

		if err := s.SetValidatorInactivityScore(int(validatorIndex), score); err != nil {
			return err
		}
		return nil
	})
}
