// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Pending attestation unit for the solid package.
// Defines the PendingAttestation types.
// Exports helpers such as EncodingSizeSSZ, DecodeSSZ, EncodeSSZ, and
// HashSSZ.
// Fixed-layout SSZ containers with in-place encoding.

//go:build n42el

package solid

import (
	"encoding/json"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

type PendingAttestation struct {
	AggregationBits *BitList         `json:"aggregation_bits"`
	Data            *AttestationData `json:"attestation_data"`
	InclusionDelay  uint64           `json:"inclusion_delay,string"`
	ProposerIndex   uint64           `json:"proposer_index,string"`
}

func (a *PendingAttestation) EncodingSizeSSZ() (size int) {
	size = 4 + AttestationDataSize + 2*length.BlockNum // 4 bytes for the length of the size offset
	if a == nil || a.AggregationBits == nil {
		return
	}
	return size + a.AggregationBits.EncodingSizeSSZ()
}

func (a *PendingAttestation) DecodeSSZ(buf []byte, _ int) error {
	if len(buf) < a.EncodingSizeSSZ() {
		return ssz.ErrLowBufferSize
	}
	a.AggregationBits = NewBitList(0, 2048)
	a.Data = &AttestationData{}
	return ssz2.UnmarshalSSZ(buf, 0, a.AggregationBits, a.Data, &a.InclusionDelay, &a.ProposerIndex)
}

func (a *PendingAttestation) EncodeSSZ(dst []byte) ([]byte, error) {
	if a.Data == nil {
		return nil, errors.New("attestation data is nil")
	}
	return ssz2.MarshalSSZ(dst, a.AggregationBits, a.Data, a.InclusionDelay, a.ProposerIndex)
}

func (a *PendingAttestation) HashSSZ() (o [32]byte, err error) {
	return merkle_tree.HashTreeRoot(a.AggregationBits, a.Data, a.InclusionDelay, a.ProposerIndex)
}

func (*PendingAttestation) Clone() clonable.Clonable {
	return &PendingAttestation{}
}

// Implement custom json unmarshalling for Attestation.
func (p *PendingAttestation) UnmarshalJSON(data []byte) error {
	// Unmarshal as normal into a temporary struct
	type tempPendingAttestation struct {
		AggregationBits *BitList         `json:"aggregation_bits"`
		Data            *AttestationData `json:"attestation_data"`
		InclusionDelay  uint64           `json:"inclusion_delay,string"`
		ProposerIndex   uint64           `json:"proposer_index,string"`
	}
	var temp tempPendingAttestation
	temp.AggregationBits = NewBitList(0, 2048)
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}
	// Copy the temporary struct into the actual struct
	p.AggregationBits = temp.AggregationBits
	p.Data = temp.Data
	p.InclusionDelay = temp.InclusionDelay
	p.ProposerIndex = temp.ProposerIndex
	return nil
}
