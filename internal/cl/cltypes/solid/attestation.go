// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Attestation unit for the solid package.
// Defines the Attestation and SingleAttestation types.
// Exports helpers such as GetCommitteeIndexFromBits, SetBeaconConfig,
// Static, and Copy.
// Fixed-layout SSZ containers with in-place encoding.

//go:build n42el

package solid

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

const (
	maxValidatorsPerCommittee  = 2048
	aggregationBitsSizeDeneb   = maxValidatorsPerCommittee
	aggregationBitsSizeElectra = 64 * maxValidatorsPerCommittee // mainnet MAX_COMMITTEES_PER_SLOT * MAX_VALIDATORS_PER_COMMITTEE
)

// Attestation type represents a statement or confirmation of some occurrence or phenomenon.
type Attestation struct {
	AggregationBits *BitList         `json:"aggregation_bits"`
	Data            *AttestationData `json:"data"`
	Signature       common.Bytes96   `json:"signature"`
	CommitteeBits   *BitVector       `json:"committee_bits,omitempty"` // Electra EIP-7549
}

func (a *Attestation) GetCommitteeIndexFromBits() (uint64, error) {
	bits := a.CommitteeBits.GetOnIndices()
	if len(bits) == 0 {
		return 0, errors.New("no committee bits set in electra attestation")
	}
	return uint64(bits[0]), nil
}

// SetBeaconConfig sets the beacon config for preset-aware hash computation.
// This must be called on Electra attestations before computing HashSSZ when the
// AggregationBits limit needs to match a specific preset (e.g. minimal vs mainnet).
func (a *Attestation) SetBeaconConfig(cfg *clparams.BeaconChainConfig) {
	if a == nil || cfg == nil || a.AggregationBits == nil || a.CommitteeBits == nil {
		return
	}
	a.AggregationBits.SetLimit(int(cfg.MaxCommitteesPerSlot) * maxValidatorsPerCommittee)
}

// Static returns whether the attestation is static or not. For Attestation, it's always false.
func (*Attestation) Static() bool {
	return false
}

func (a *Attestation) Copy() *Attestation {
	new := &Attestation{}
	new.AggregationBits = a.AggregationBits.Copy()
	new.Data = &AttestationData{}
	*new.Data = *a.Data
	copy(new.Signature[:], a.Signature[:])
	new.CommitteeBits = a.CommitteeBits.Copy()
	return new
}

// EncodingSizeSSZ returns the size of the Attestation instance when encoded in SSZ format.
func (a *Attestation) EncodingSizeSSZ() (size int) {
	if a.CommitteeBits != nil {
		// Electra case
		return 4 + AttestationDataSize + length.Bytes96 +
			a.CommitteeBits.EncodingSizeSSZ() +
			a.AggregationBits.EncodingSizeSSZ()
	}
	// Deneb case
	size = AttestationDataSize + length.Bytes96
	if a == nil || a.AggregationBits == nil {
		return
	}
	return size + a.AggregationBits.EncodingSizeSSZ() + 4 // 4 bytes for the length of the size offset
}

// DecodeSSZ decodes the provided buffer into the Attestation instance.
func (a *Attestation) DecodeSSZ(buf []byte, version int) error {
	clversion := clparams.StateVersion(version)
	if clversion.AfterOrEqual(clparams.ElectraVersion) {
		// The CommitteeBits size depends on MAX_COMMITTEES_PER_SLOT which differs between
		// mainnet (64) and the minimal preset (4). Instead of hardcoding 64, infer the
		// CommitteeBits byte count from the SSZ offset table.
		// Layout: [4-byte offset][AttestationData][Signature][CommitteeBits][AggregationBits]
		const electraFixedHeaderSize = 4 + AttestationDataSize + length.Bytes96
		if len(buf) < electraFixedHeaderSize+1 {
			return ssz.ErrLowBufferSize
		}
		aggrBitsOffset := int(binary.LittleEndian.Uint32(buf[:4]))
		committeeBitsBytes := aggrBitsOffset - electraFixedHeaderSize
		if committeeBitsBytes <= 0 {
			return ssz.ErrLowBufferSize
		}
		a.AggregationBits = NewBitList(0, aggregationBitsSizeElectra)
		a.Data = &AttestationData{}
		a.CommitteeBits = NewBitVector(committeeBitsBytes * 8)
		return ssz2.UnmarshalSSZ(buf, version, a.AggregationBits, a.Data, a.Signature[:], a.CommitteeBits)
	}

	// Deneb case
	if len(buf) < a.EncodingSizeSSZ() {
		return ssz.ErrLowBufferSize
	}
	a.AggregationBits = NewBitList(0, aggregationBitsSizeDeneb)
	a.Data = &AttestationData{}
	return ssz2.UnmarshalSSZ(buf, version, a.AggregationBits, a.Data, a.Signature[:])
}

// EncodeSSZ encodes the Attestation instance into the provided buffer.
func (a *Attestation) EncodeSSZ(dst []byte) ([]byte, error) {
	if a.CommitteeBits != nil {
		// Electra case
		return ssz2.MarshalSSZ(dst, a.AggregationBits, a.Data, a.Signature[:], a.CommitteeBits)
	}
	return ssz2.MarshalSSZ(dst, a.AggregationBits, a.Data, a.Signature[:])
}

// HashSSZ hashes the Attestation instance using SSZ.
func (a *Attestation) HashSSZ() (o [32]byte, err error) {
	if a.CommitteeBits != nil {
		// Electra case
		return merkle_tree.HashTreeRoot(a.AggregationBits, a.Data, a.Signature[:], a.CommitteeBits)
	}
	return merkle_tree.HashTreeRoot(a.AggregationBits, a.Data, a.Signature[:])
}

// Clone creates a new clone of the Attestation instance.
func (a *Attestation) Clone() clonable.Clonable {
	return &Attestation{}
}

// Implement custom json unmarshalling for Attestation.
func (a *Attestation) UnmarshalJSON(data []byte) error {
	// Unmarshal as normal into a temporary struct
	type tempAttestation struct {
		AggregationBits *BitList         `json:"aggregation_bits"`
		Data            *AttestationData `json:"data"`
		Signature       common.Bytes96   `json:"signature"`
		CommitteeBits   *BitVector       `json:"committee_bits,omitempty"`
	}

	// For Electra, the committee bits are present in the JSON
	if bytes.Contains(data, []byte("committee_bits")) {
		// Electra case
		var temp tempAttestation
		temp.AggregationBits = NewBitList(0, aggregationBitsSizeElectra)
		temp.CommitteeBits = &BitVector{} // UnmarshalJSON self-sizes from the hex bytes
		if err := json.Unmarshal(data, &temp); err != nil {
			return err
		}
		a.AggregationBits = temp.AggregationBits
		a.Data = temp.Data
		a.Signature = temp.Signature
		a.CommitteeBits = temp.CommitteeBits
		return nil
	}

	// Deneb case
	var temp tempAttestation
	temp.AggregationBits = NewBitList(0, aggregationBitsSizeDeneb)
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}
	// Copy the temporary struct into the actual struct
	a.AggregationBits = temp.AggregationBits
	a.Data = temp.Data
	a.Signature = temp.Signature
	return nil
}

// class SingleAttestation(Container):
//
//	committee_index: CommitteeIndex
//	attester_index: ValidatorIndex
//	data: AttestationData
//	signature: BLSSignature
type SingleAttestation struct {
	CommitteeIndex uint64           `json:"committee_index,string"`
	AttesterIndex  uint64           `json:"attester_index,string"`
	Data           *AttestationData `json:"data"`
	Signature      common.Bytes96   `json:"signature"`
}

func (s *SingleAttestation) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, &s.CommitteeIndex, &s.AttesterIndex, s.Data, s.Signature[:])
}

func (s *SingleAttestation) DecodeSSZ(buf []byte, version int) error {
	s.Data = &AttestationData{}
	return ssz2.UnmarshalSSZ(buf, version, &s.CommitteeIndex, &s.AttesterIndex, s.Data, s.Signature[:])
}

func (s *SingleAttestation) EncodingSizeSSZ() (size int) {
	return 8 + 8 + AttestationDataSize + length.Bytes96
}

func (s *SingleAttestation) HashSSZ() (o [32]byte, err error) {
	return merkle_tree.HashTreeRoot(&s.CommitteeIndex, &s.AttesterIndex, s.Data, s.Signature[:])
}

func (s *SingleAttestation) Clone() clonable.Clonable {
	return &SingleAttestation{
		Data: &AttestationData{},
	}
}

func (s *SingleAttestation) Static() bool {
	return true
}

func (s *SingleAttestation) ToAttestation(memberIndexInCommittee int, committeeLen int, maxCommittees int) *Attestation {
	committeeBits := NewBitVector(maxCommittees)
	committeeBits.SetBitAt(int(s.CommitteeIndex), true)
	// flip the bit for the validator and also mark the last bit
	bytes := make([]byte, committeeLen/8+1)
	bytes[memberIndexInCommittee/8] |= 1 << (memberIndexInCommittee % 8)
	bytes[committeeLen/8] |= 1 << (committeeLen % 8)
	aggregationBits := BitlistFromBytes(bytes, aggregationBitsSizeElectra)
	return &Attestation{
		AggregationBits: aggregationBits,
		Data:            s.Data,
		Signature:       s.Signature,
		CommitteeBits:   committeeBits,
	}
}

func (s *SingleAttestation) AttestationData() *AttestationData {
	return s.Data
}
