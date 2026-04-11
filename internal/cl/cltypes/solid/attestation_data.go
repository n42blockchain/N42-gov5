// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Attestation data unit for the solid package.
// Defines the AttestationData types.
// Exports helpers such as Static, EncodingSizeSSZ, DecodeSSZ, and EncodeSSZ.
// Fixed-layout SSZ containers with in-place encoding.

//go:build n42el

package solid

import (
	"bytes"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

const AttestationDataSize = 128

// AttestationData contains information about attestantion, including finalized/attested checkpoints.
type AttestationData struct {
	Slot           uint64 `json:"slot,string"`
	CommitteeIndex uint64 `json:"index,string"` // CommitteeIndex will be deprecated and always equal to 0 after Electra
	// LMD GHOST vote
	BeaconBlockRoot common.Hash `json:"beacon_block_root"`
	// FFG vote
	Source Checkpoint `json:"source"`
	Target Checkpoint `json:"target"`
}

func (a *AttestationData) Static() bool {
	return true
}

func (a *AttestationData) EncodingSizeSSZ() int {
	return AttestationDataSize
}

func (a *AttestationData) DecodeSSZ(buf []byte, version int) error {
	return ssz2.UnmarshalSSZ(buf, version, &a.Slot, &a.CommitteeIndex, a.BeaconBlockRoot[:], &a.Source, &a.Target)
}

func (a *AttestationData) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, a.Slot, a.CommitteeIndex, a.BeaconBlockRoot[:], &a.Source, &a.Target)
}

func (a *AttestationData) Clone() clonable.Clonable {
	return &AttestationData{}
}

func (a *AttestationData) HashSSZ() (o [32]byte, err error) {
	return merkle_tree.HashTreeRoot(a.Slot, a.CommitteeIndex, a.BeaconBlockRoot[:], &a.Source, &a.Target)
}

func (a *AttestationData) Equal(other *AttestationData) bool {
	return a.Slot == other.Slot && a.CommitteeIndex == other.CommitteeIndex && bytes.Equal(a.BeaconBlockRoot[:], other.BeaconBlockRoot[:]) &&
		a.Source.Equal(other.Source) && a.Target.Equal(other.Target)
}
