// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Resets unit for the statechange package.
// Exports helpers such as ProcessEth1DataReset, ProcessSlashingsReset,
// ProcessRandaoMixesReset, and ProcessParticipationFlagUpdates.
// Per-slot and per-epoch state change routines.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
)

func ProcessEth1DataReset(s abstract.BeaconState) {
	nextEpoch := state.Epoch(s) + 1
	if nextEpoch%s.BeaconConfig().EpochsPerEth1VotingPeriod == 0 {
		s.ResetEth1DataVotes()
	}
}

func ProcessSlashingsReset(s abstract.BeaconState) {
	s.SetSlashingSegmentAt(int(state.Epoch(s)+1)%int(s.BeaconConfig().EpochsPerSlashingsVector), 0)

}

func ProcessRandaoMixesReset(s abstract.BeaconState) {
	currentEpoch := state.Epoch(s)
	nextEpoch := state.Epoch(s) + 1
	s.SetRandaoMixAt(int(nextEpoch%s.BeaconConfig().EpochsPerHistoricalVector), s.GetRandaoMixes(currentEpoch))
}

func ProcessParticipationFlagUpdates(state abstract.BeaconState) {
	defer monitor.ObserveElaspedTime(monitor.ProcessParticipationFlagUpdatesTime).End()
	state.ResetEpochParticipation()
}
