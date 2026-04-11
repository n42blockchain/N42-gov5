// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Copy unit for the raw package.
// Exports helpers such as CopyInto and Copy.
// Part of the n42el consensus-layer build.

//go:build n42el

package raw

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
)

func (b *BeaconState) CopyInto(dst *BeaconState) error {
	dst.genesisTime = b.genesisTime
	dst.genesisValidatorsRoot = b.genesisValidatorsRoot
	dst.slot = b.slot
	dst.fork = b.fork.Copy()
	dst.latestBlockHeader = b.latestBlockHeader.Copy()
	b.blockRoots.CopyTo(dst.blockRoots)
	b.stateRoots.CopyTo(dst.stateRoots)
	b.historicalRoots.CopyTo(dst.historicalRoots)
	dst.eth1Data = b.eth1Data.Copy()
	dst.eth1DataVotes = solid.NewStaticListSSZ[*cltypes.Eth1Data](int(b.beaconConfig.Eth1DataVotesLength()), 72)
	b.eth1DataVotes.Range(func(index int, value *cltypes.Eth1Data, length int) bool {
		dst.eth1DataVotes.Append(value.Copy())
		return true
	})
	dst.eth1DepositIndex = b.eth1DepositIndex
	b.validators.CopyTo(dst.validators)
	b.balances.CopyTo(dst.balances)
	b.randaoMixes.CopyTo(dst.randaoMixes)
	b.slashings.CopyTo(dst.slashings)
	b.previousEpochParticipation.CopyTo(dst.previousEpochParticipation)
	b.currentEpochParticipation.CopyTo(dst.currentEpochParticipation)
	dst.currentEpochAttestations.Clear()
	dst.previousEpochAttestations.Clear()
	b.currentEpochAttestations.Range(func(index int, value *solid.PendingAttestation, length int) bool {
		dst.currentEpochAttestations.Append(value)
		return true
	})
	b.previousEpochAttestations.Range(func(index int, value *solid.PendingAttestation, length int) bool {
		dst.previousEpochAttestations.Append(value)
		return true
	})
	dst.finalizedCheckpoint = b.finalizedCheckpoint
	dst.currentJustifiedCheckpoint = b.currentJustifiedCheckpoint
	dst.previousJustifiedCheckpoint = b.previousJustifiedCheckpoint
	dst.justificationBits = b.justificationBits.Copy()
	if b.version == clparams.Phase0Version {
		dst.init()
		return nil
	}
	dst.currentSyncCommittee = b.currentSyncCommittee.Copy()
	dst.nextSyncCommittee = b.nextSyncCommittee.Copy()
	b.inactivityScores.CopyTo(dst.inactivityScores)

	if b.version >= clparams.BellatrixVersion {
		dst.latestExecutionPayloadHeader = b.latestExecutionPayloadHeader.Copy()
	}
	dst.nextWithdrawalIndex = b.nextWithdrawalIndex
	dst.nextWithdrawalValidatorIndex = b.nextWithdrawalValidatorIndex
	dst.historicalSummaries = solid.NewStaticListSSZ[*cltypes.HistoricalSummary](int(b.beaconConfig.HistoricalRootsLimit), 64)
	b.historicalSummaries.Range(func(_ int, value *cltypes.HistoricalSummary, _ int) bool {
		dst.historicalSummaries.Append(value)
		return true
	})
	if b.version >= clparams.ElectraVersion {
		// Electra fields
		dst.depositRequestsStartIndex = b.depositRequestsStartIndex
		dst.depositBalanceToConsume = b.depositBalanceToConsume
		dst.exitBalanceToConsume = b.exitBalanceToConsume
		dst.earliestExitEpoch = b.earliestExitEpoch
		dst.consolidationBalanceToConsume = b.consolidationBalanceToConsume
		dst.earliestConsolidationEpoch = b.earliestConsolidationEpoch
		dst.pendingDeposits = b.pendingDeposits.ShallowCopy()
		dst.pendingPartialWithdrawals = b.pendingPartialWithdrawals.ShallowCopy()
		dst.pendingConsolidations = b.pendingConsolidations.ShallowCopy()
	}

	if b.version >= clparams.FuluVersion {
		dst.proposerLookahead = b.proposerLookahead
	}

	dst.version = b.version
	// Now sync internals
	copy(dst.leaves, b.leaves)
	dst.touchedLeaves = make([]atomic.Uint32, StateLeafSizeLatest)
	for leafIndex := range b.touchedLeaves {
		// Copy the value
		dst.touchedLeaves[leafIndex].Store(b.touchedLeaves[leafIndex].Load())
	}
	return nil
}

func (b *BeaconState) Copy() (*BeaconState, error) {
	copied := New(b.BeaconConfig())
	return copied, b.CopyInto(copied)
}
