// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Beacon committee subscriptions unit for the cltypes package.
// Defines the BeaconCommitteeSubscription types.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

type BeaconCommitteeSubscription struct {
	ValidatorIndex   uint64 `json:"validator_index,string"`
	CommitteeIndex   uint64 `json:"committee_index,string"`
	CommitteesAtSlot uint64 `json:"committees_at_slot,string"`
	Slot             uint64 `json:"slot,string"`
	IsAggregator     bool   `json:"is_aggregator"`
}
