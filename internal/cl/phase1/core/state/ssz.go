// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package state

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/metrics"
)

func (b *CachingBeaconState) EncodeSSZ(buf []byte) ([]byte, error) {
	bts, err := b.BeaconState.EncodeSSZ(buf)
	if err != nil {
		return nil, err
	}
	sz := metrics.NewHistTimer("encode_ssz_beacon_state_size")
	sz.Observe(float64(len(bts)))
	return bts, nil
}

func (b *CachingBeaconState) DecodeSSZ(buf []byte, version int) error {
	if err := b.BeaconState.DecodeSSZ(buf, version); err != nil {
		return err
	}
	sz := metrics.NewHistTimer("decode_ssz_beacon_state_size")
	sz.Observe(float64(len(buf)))
	return b.InitBeaconState()
}

// SSZ size of the Beacon State
func (b *CachingBeaconState) EncodingSizeSSZ() (size int) {
	sz := b.BeaconState.EncodingSizeSSZ()
	return sz
}

func (b *CachingBeaconState) Clone() clonable.Clonable {
	return New(b.BeaconConfig())
}
