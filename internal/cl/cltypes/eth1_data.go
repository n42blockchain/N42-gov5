// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"

	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

type Eth1Data struct {
	Root         common.Hash `json:"deposit_root"`
	DepositCount uint64      `json:"deposit_count,string"`
	BlockHash    common.Hash `json:"block_hash"`
}

func NewEth1Data() *Eth1Data {
	return &Eth1Data{}
}

func (e *Eth1Data) Copy() *Eth1Data {
	return &Eth1Data{
		Root:         e.Root,
		DepositCount: e.DepositCount,
		BlockHash:    e.BlockHash,
	}
}

func (e *Eth1Data) Equal(b *Eth1Data) bool {
	return e.BlockHash == b.BlockHash && e.Root == b.Root && b.DepositCount == e.DepositCount
}

// MarshalSSZTo ssz marshals the Eth1Data object to a target array
func (e *Eth1Data) EncodeSSZ(buf []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(buf, e.Root[:], e.DepositCount, e.BlockHash[:])

}

func (e *Eth1Data) DecodeSSZ(buf []byte, _ int) error {
	return ssz2.UnmarshalSSZ(buf, 0, e.Root[:], &e.DepositCount, e.BlockHash[:])
}

// EncodingSizeSSZ returns the ssz encoded size in bytes for the Eth1Data object
func (e *Eth1Data) EncodingSizeSSZ() int {
	return 8 + length.Hash*2
}

// HashSSZ ssz hashes the Eth1Data object
func (e *Eth1Data) HashSSZ() ([32]byte, error) {
	return merkle_tree.HashTreeRoot(e.Root[:], e.DepositCount, e.BlockHash[:])
}

func (e *Eth1Data) Static() bool {
	return true
}
