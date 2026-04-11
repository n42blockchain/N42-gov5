// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package synced_data

import (
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

type CancelFn func()
type ViewHeadStateFn func(headState *state.CachingBeaconState) error

//go:generate mockgen -typed=true -destination=./mock_services/synced_data_mock.go -package=mock_services . SyncedData
type SyncedData interface {
	OnHeadState(newState *state.CachingBeaconState) error
	UnsetHeadState()
	ViewHeadState(fn ViewHeadStateFn) error
	ViewPreviousHeadState(fn ViewHeadStateFn) error
	Syncing() bool
	HeadSlot() uint64
	HeadRoot() common.Hash
	CommitteeCount(epoch uint64) uint64
	ValidatorPublicKeyByIndex(index int) (common.Bytes48, error)
	ValidatorIndexByPublicKey(pubkey common.Bytes48) (uint64, bool, error)
	HistoricalRootElementAtIndex(index int) (common.Hash, error)
	HistoricalSummaryElementAtIndex(index int) (*cltypes.HistoricalSummary, error)
}
