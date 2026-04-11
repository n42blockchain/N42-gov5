// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Checkpoint unit for the solid package.
// Defines the Checkpoint types.
// Exports helpers such as EncodingSizeSSZ, DecodeSSZ, EncodeSSZ, and Clone.
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

var _ ssz2.SizedObjectSSZ = (*Checkpoint)(nil)

const CheckpointSizeSSZ = 40

type Checkpoint struct {
	Epoch uint64      `json:"epoch,string"`
	Root  common.Hash `json:"root"`
}

// EncodingSizeSSZ returns the size of the Checkpoint object when encoded as SSZ.
func (*Checkpoint) EncodingSizeSSZ() int {
	return CheckpointSizeSSZ
}

// DecodeSSZ decodes the Checkpoint object from SSZ-encoded data.
func (c *Checkpoint) DecodeSSZ(buf []byte, version int) error {
	return ssz2.UnmarshalSSZ(buf, version, &c.Epoch, c.Root[:])
}

// EncodeSSZ encodes the Checkpoint object into SSZ format.
func (c *Checkpoint) EncodeSSZ(dst []byte) ([]byte, error) {
	return ssz2.MarshalSSZ(dst, c.Epoch, c.Root[:])
}

// Clone returns a new Checkpoint object that is a copy of the current object.
func (c *Checkpoint) Clone() clonable.Clonable {
	return &Checkpoint{}
}

// Equal checks if the Checkpoint object is equal to another Checkpoint object.
func (c *Checkpoint) Equal(other Checkpoint) bool {
	return c.Epoch == other.Epoch && bytes.Equal(c.Root[:], other.Root[:])
}

// Copy returns a copy of the Checkpoint object.
func (c *Checkpoint) Copy() *Checkpoint {
	return &Checkpoint{
		Epoch: c.Epoch,
		Root:  c.Root,
	}
}

// HashSSZ returns the hash of the Checkpoint object when encoded as SSZ.
func (c Checkpoint) HashSSZ() (o [32]byte, err error) {
	return merkle_tree.HashTreeRoot(c.Epoch, c.Root[:])
}

// Static always returns true, indicating that the Checkpoint object is static.
func (c Checkpoint) Static() bool {
	return true
}
