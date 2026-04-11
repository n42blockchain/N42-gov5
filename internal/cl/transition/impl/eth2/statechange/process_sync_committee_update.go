// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package statechange

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
)

// ProcessSyncCommitteeUpdate implements processing for the sync committee update. unfortunately there is no easy way to test it.
func ProcessSyncCommitteeUpdate(s abstract.BeaconState) error {
	defer monitor.ObserveElaspedTime(monitor.ProcessSyncCommitteeUpdateTime).End()
	if (state.Epoch(s)+1)%s.BeaconConfig().EpochsPerSyncCommitteePeriod != 0 {
		return nil
	}
	// Set new current sync committee.
	s.SetCurrentSyncCommittee(s.NextSyncCommittee())
	// Compute next new sync committee
	committee, err := s.ComputeNextSyncCommittee()
	if err != nil {
		return err
	}
	s.SetNextSyncCommittee(committee)
	return nil
}
