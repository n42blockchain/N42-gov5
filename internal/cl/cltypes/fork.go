// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Fork unit for the cltypes package.
// Defines the Fork types.
// Exports helpers such as Static, Copy, EncodeSSZ, and DecodeSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

// Fork data, contains if we were on bellatrix/alteir/phase0 and transition epoch.
type Fork struct {
	PreviousVersion common.Bytes4 `json:"previous_version"`
	CurrentVersion  common.Bytes4 `json:"current_version"`
	Epoch           uint64        `json:"epoch,string"`
}

func (*Fork) Static() bool {
	return true
}

func (f *Fork) Copy() *Fork {
	return &Fork{
		PreviousVersion: f.PreviousVersion,
		CurrentVersion:  f.CurrentVersion,
		Epoch:           f.Epoch,
	}
}

func (f *Fork) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, f.PreviousVersion[:], f.CurrentVersion[:], f.Epoch)
}

func (f *Fork) DecodeSSZ(buf []byte, _ int) error {
	return ssz2.UnmarshalSSZ(buf, 0, f.PreviousVersion[:], f.CurrentVersion[:], &f.Epoch)

}

func (f *Fork) EncodingSizeSSZ() int {
	return 16
}

func (f *Fork) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(f.PreviousVersion[:], f.CurrentVersion[:], f.Epoch)
}
