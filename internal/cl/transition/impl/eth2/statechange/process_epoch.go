// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Process epoch unit for the statechange package.
// Exports helpers such as GetUnslashedIndiciesSet and ProcessEpoch.
// Per-slot and per-epoch state change routines.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/utils/threading"
)

func GetUnslashedIndiciesSet(cfg *clparams.BeaconChainConfig, previousEpoch uint64, validatorSet *solid.ValidatorSet, previousEpochParticipation *solid.ParticipationBitList) [][]bool {
	weights := cfg.ParticipationWeights()
	flagsUnslashedIndiciesSet := make([][]bool, len(weights))
	for i := range weights {
		flagsUnslashedIndiciesSet[i] = make([]bool, validatorSet.Length())
	}

	threading.ParallellForLoop(1, 0, validatorSet.Length(), func(validatorIndex int) error {
		for i := range weights {
			flagsUnslashedIndiciesSet[i][validatorIndex] = state.IsUnslashedParticipatingIndex(validatorSet, previousEpochParticipation, previousEpoch, uint64(validatorIndex), i)
		}
		return nil
	})

	return flagsUnslashedIndiciesSet
}

// ProcessEpoch process epoch transition.
func ProcessEpoch(s abstract.BeaconState) error {
	defer monitor.ObserveElaspedTime(monitor.EpochProcessingTime).End()
	eligibleValidators := state.EligibleValidatorsIndicies(s)
	var unslashedIndiciesSet [][]bool
	if s.Version() >= clparams.AltairVersion {
		unslashedIndiciesSet = GetUnslashedIndiciesSet(s.BeaconConfig(), state.PreviousEpoch(s), s.ValidatorSet(), s.PreviousEpochParticipation())
	}
	if err := ProcessJustificationBitsAndFinality(s, unslashedIndiciesSet); err != nil {
		return err
	}

	if s.Version() >= clparams.AltairVersion {
		if err := ProcessInactivityScores(s, eligibleValidators, unslashedIndiciesSet); err != nil {
			return err
		}
	}

	if err := ProcessRewardsAndPenalties(s, eligibleValidators, unslashedIndiciesSet); err != nil {
		return err
	}

	if err := ProcessRegistryUpdates(s); err != nil {
		return err
	}

	if err := ProcessSlashings(s); err != nil {
		return err
	}

	ProcessEth1DataReset(s)
	if s.Version() >= clparams.ElectraVersion {
		ProcessPendingDeposits(s)
		ProcessPendingConsolidations(s)
	}

	if err := ProcessEffectiveBalanceUpdates(s); err != nil {
		return err
	}

	ProcessSlashingsReset(s)
	ProcessRandaoMixesReset(s)

	if err := ProcessHistoricalRootsUpdate(s); err != nil {
		return err
	}

	if s.Version() == clparams.Phase0Version {
		if err := ProcessParticipationRecordUpdates(s); err != nil {
			return err
		}
	}

	if s.Version() >= clparams.AltairVersion {
		ProcessParticipationFlagUpdates(s)
		if err := ProcessSyncCommitteeUpdate(s); err != nil {
			return err
		}
	}

	if s.Version() >= clparams.FuluVersion {
		if err := ProcessProposerLookahead(s); err != nil {
			return err
		}
	}

	return nil
}
