// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Slashings unit for the cltypes package.
// Defines the ProposerSlashing and AttesterSlashing types.
// Provides constructors NewAttesterSlashing.
// Exports helpers such as EncodeSSZ, DecodeSSZ, EncodingSizeSSZ, and
// HashSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

type ProposerSlashing struct {
	Header1 *SignedBeaconBlockHeader `json:"signed_header_1"`
	Header2 *SignedBeaconBlockHeader `json:"signed_header_2"`
}

func (p *ProposerSlashing) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, p.Header1, p.Header2)
}

func (p *ProposerSlashing) DecodeSSZ(buf []byte, version int) error {
	p.Header1 = new(SignedBeaconBlockHeader)
	p.Header2 = new(SignedBeaconBlockHeader)
	return ssz2.UnmarshalSSZ(buf, version, p.Header1, p.Header2)
}

func (p *ProposerSlashing) EncodingSizeSSZ() int {
	return p.Header1.EncodingSizeSSZ() * 2
}

func (p *ProposerSlashing) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(p.Header1, p.Header2)
}

type AttesterSlashing struct {
	Attestation_1 *IndexedAttestation `json:"attestation_1"`
	Attestation_2 *IndexedAttestation `json:"attestation_2"`
}

func NewAttesterSlashing(version clparams.StateVersion) *AttesterSlashing {
	return &AttesterSlashing{
		Attestation_1: NewIndexedAttestation(version),
		Attestation_2: NewIndexedAttestation(version),
	}
}

func (a *AttesterSlashing) SetVersion(v clparams.StateVersion) {
	a.Attestation_1.SetVersion(v)
	a.Attestation_2.SetVersion(v)
}

func (a *AttesterSlashing) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, a.Attestation_1, a.Attestation_2)
}

func (a *AttesterSlashing) DecodeSSZ(buf []byte, version int) error {
	a.Attestation_1 = NewIndexedAttestation(clparams.StateVersion(version))
	a.Attestation_2 = NewIndexedAttestation(clparams.StateVersion(version))
	return ssz2.UnmarshalSSZ(buf, version, a.Attestation_1, a.Attestation_2)
}

func (a *AttesterSlashing) EncodingSizeSSZ() int {
	return 8 + a.Attestation_1.EncodingSizeSSZ() + a.Attestation_2.EncodingSizeSSZ()
}

func (a *AttesterSlashing) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(a.Attestation_1, a.Attestation_2)
}
