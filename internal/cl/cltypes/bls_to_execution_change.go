// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bls to execution change unit for the cltypes package.
// Defines the BLSToExecutionChange and SignedBLSToExecutionChange types.
// Exports helpers such as EncodeSSZ, HashSSZ, DecodeSSZ, and
// EncodingSizeSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

// Change to EL engine
type BLSToExecutionChange struct {
	ValidatorIndex uint64         `json:"validator_index,string"`
	From           common.Bytes48 `json:"from_bls_pubkey"`
	To             common.Address `json:"to_execution_address"`
}

func (b *BLSToExecutionChange) EncodeSSZ(buf []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(buf, b.ValidatorIndex, b.From[:], b.To[:])
}

func (b *BLSToExecutionChange) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(b.ValidatorIndex, b.From[:], b.To[:])
}

func (b *BLSToExecutionChange) DecodeSSZ(buf []byte, version int) error {
	if len(buf) < b.EncodingSizeSSZ() {
		return fmt.Errorf("[BLSToExecutionChange] err: %s", ssz.ErrLowBufferSize)
	}
	b.ValidatorIndex = ssz.UnmarshalUint64SSZ(buf)
	copy(b.From[:], buf[8:])
	copy(b.To[:], buf[56:])
	return ssz2.UnmarshalSSZ(buf, version, &b.ValidatorIndex, b.From[:], b.To[:])
}

func (*BLSToExecutionChange) EncodingSizeSSZ() int {
	return 76
}

func (*BLSToExecutionChange) Static() bool {
	return true
}

type SignedBLSToExecutionChange struct {
	Message   *BLSToExecutionChange `json:"message"`
	Signature common.Bytes96        `json:"signature"`
}

func (s *SignedBLSToExecutionChange) EncodeSSZ(buf []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(buf, s.Message, s.Signature[:])
}

func (s *SignedBLSToExecutionChange) DecodeSSZ(buf []byte, version int) error {
	s.Message = new(BLSToExecutionChange)
	return ssz2.UnmarshalSSZ(buf, version, s.Message, s.Signature[:])
}

func (s *SignedBLSToExecutionChange) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(s.Message, s.Signature[:])
}

func (s *SignedBLSToExecutionChange) EncodingSizeSSZ() int {
	return 96 + s.Message.EncodingSizeSSZ()
}
