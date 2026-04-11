// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Sync aggregator selection data unit for the cltypes package.
// Defines the SyncAggregatorSelectionData types.
// Exports helpers such as Static, Copy, EncodeSSZ, and DecodeSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

// SyncAggregatorSelectionData data, contains if we were on bellatrix/alteir/phase0 and transition epoch.
type SyncAggregatorSelectionData struct {
	Slot              uint64 `json:"slot,string"`
	SubcommitteeIndex uint64 `json:"subcommittee_index,string"`
}

func (*SyncAggregatorSelectionData) Static() bool {
	return true
}

func (f *SyncAggregatorSelectionData) Copy() *SyncAggregatorSelectionData {
	return &SyncAggregatorSelectionData{
		Slot:              f.Slot,
		SubcommitteeIndex: f.SubcommitteeIndex,
	}
}

func (f *SyncAggregatorSelectionData) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, f.Slot, f.SubcommitteeIndex)
}

func (f *SyncAggregatorSelectionData) DecodeSSZ(buf []byte, _ int) error {
	return ssz2.UnmarshalSSZ(buf, 0, &f.Slot, &f.SubcommitteeIndex)

}

func (f *SyncAggregatorSelectionData) EncodingSizeSSZ() int {
	return 16
}

func (f *SyncAggregatorSelectionData) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(f.Slot, f.SubcommitteeIndex)
}

func (f *SyncAggregatorSelectionData) Clone() clonable.Clonable {
	return &SyncAggregatorSelectionData{
		Slot:              f.Slot,
		SubcommitteeIndex: f.SubcommitteeIndex,
	}
}
