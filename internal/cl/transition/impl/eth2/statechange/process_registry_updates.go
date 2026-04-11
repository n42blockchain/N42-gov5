// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package statechange

import (
	"sort"
	"sync"

	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/monitor"
	"github.com/n42blockchain/N42/internal/cl/utils/threading"

	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"

	"github.com/n42blockchain/N42/internal/cl/clparams"
)

// computeActivationExitEpoch is Implementation of compute_activation_exit_epoch. Defined in https://github.com/ethereum/consensus-specs/blob/dev/specs/phase0/beacon-chain.md#compute_activation_exit_epoch.
func computeActivationExitEpoch(beaconConfig *clparams.BeaconChainConfig, epoch uint64) uint64 {
	return epoch + 1 + beaconConfig.MaxSeedLookahead
}

type minimizeQueuedValidator struct {
	validatorIndex             uint64
	activationEligibilityEpoch uint64
}

// ProcessRegistryUpdates updates every epoch the activation status of validators. Specs at: https://github.com/ethereum/consensus-specs/blob/dev/specs/phase0/beacon-chain.md#registry-updates.
func ProcessRegistryUpdates(s abstract.BeaconState) error {
	defer monitor.ObserveElaspedTime(monitor.ProcessRegistryUpdatesTime).End()
	beaconConfig := s.BeaconConfig()
	currentEpoch := state.Epoch(s)
	// start also initializing the activation queue.
	var m sync.Mutex
	activationQueue := make([]minimizeQueuedValidator, 0)
	// Process activation eligibility and ejections.
	if err := threading.ParallellForLoop(1, 0, s.ValidatorSet().Length(), func(i int) error {
		validator := s.ValidatorSet().Get(i)
		if state.IsValidatorEligibleForActivationQueue(s, validator) {
			s.SetActivationEligibilityEpochForValidatorAtIndex(i, currentEpoch+1)
		}
		effectivaBalance := validator.EffectiveBalance()
		if validator.Active(currentEpoch) && effectivaBalance <= beaconConfig.EjectionBalance {
			if err := s.InitiateValidatorExit(uint64(i)); err != nil {
				return err
			}
		}
		// Insert in the activation queue in case.
		activationEligibilityEpoch := validator.ActivationEligibilityEpoch()
		if activationEligibilityEpoch <= s.FinalizedCheckpoint().Epoch &&
			validator.ActivationEpoch() == s.BeaconConfig().FarFutureEpoch {
			m.Lock()
			activationQueue = append(activationQueue, minimizeQueuedValidator{
				validatorIndex:             uint64(i),
				activationEligibilityEpoch: activationEligibilityEpoch,
			})
			m.Unlock()
		}
		return nil
	}); err != nil {
		return err
	}

	if s.Version() <= clparams.DenebVersion {
		// order the queue accordingly.
		sort.Slice(activationQueue, func(i, j int) bool {
			//  Order by the sequence of activation_eligibility_epoch setting and then index.
			if activationQueue[i].activationEligibilityEpoch != activationQueue[j].activationEligibilityEpoch {
				return activationQueue[i].activationEligibilityEpoch < activationQueue[j].activationEligibilityEpoch
			}
			return activationQueue[i].validatorIndex < activationQueue[j].validatorIndex
		})
		activationQueueLength := s.GetValidatorActivationChurnLimit()
		if len(activationQueue) > int(activationQueueLength) {
			activationQueue = activationQueue[:activationQueueLength]
		}
	}

	// Only process up to epoch limit.
	for _, entry := range activationQueue {
		s.SetActivationEpochForValidatorAtIndex(int(entry.validatorIndex), computeActivationExitEpoch(beaconConfig, currentEpoch))
	}
	return nil
}
