// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Copy unit for the state package.
// Exports helpers such as CopyInto and Copy.
// Part of the n42el consensus-layer build.

//go:build n42el

package state

import (
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/maphash"
)

func (b *CachingBeaconState) CopyInto(bs *CachingBeaconState) (err error) {
	err = b.BeaconState.CopyInto(bs.BeaconState)
	if err != nil {
		return err
	}

	err = bs.reinitCaches()
	if err != nil {
		return err
	}
	return nil
}

func (bs *CachingBeaconState) reinitCaches() error {
	if bs.Version() == clparams.Phase0Version {
		return bs.InitBeaconState()
	}

	if bs.publicKeyIndicies == nil {
		bs.publicKeyIndicies = maphash.NewNonConcurrentMap[uint64]()
	} else {
		bs.publicKeyIndicies.Clear()
	}

	bs.ForEachValidator(func(v solid.Validator, idx, total int) bool {
		bs.publicKeyIndicies.Set(v.PublicKeyBytes(), uint64(idx))
		return true
	})

	bs.totalActiveBalanceCache = nil
	bs._refreshActiveBalancesIfNeeded()
	bs.previousStateRoot = common.Hash{}
	bs.initCaches()
	if err := bs._updateProposerIndex(); err != nil {
		return err
	}
	if bs.Version() >= clparams.Phase0Version {
		return bs._initializeValidatorsPhase0()
	}

	return nil
}

func (b *CachingBeaconState) Copy() (bs *CachingBeaconState, err error) {
	copied := New(b.BeaconConfig())
	err = b.CopyInto(copied)
	if err != nil {
		return nil, err
	}
	return copied, nil
}
