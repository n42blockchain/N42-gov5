// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package solid

import (
	"encoding/json"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/length"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

type hashVector struct {
	u *hashList
}

func NewHashVector(s int) HashVectorSSZ {
	return &hashVector{
		u: &hashList{
			u: make([]byte, s*length.Hash),
			c: int(merkle_tree.NextPowerOfTwo(uint64(s))),
			l: s,
		},
	}
}

func (arr *hashVector) Bytes() []byte {
	return arr.u.u[:arr.u.l*length.Hash]
}

func (h *hashVector) Append(val common.Hash) {
	panic("not implemented")
}

func (h hashVector) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.u)
}

func (h *hashVector) UnmarshalJSON(buf []byte) error {
	return json.Unmarshal(buf, h.u)
}

func (h *hashVector) Cap() int {
	return h.u.l
}

func (h *hashVector) Length() int {
	return h.u.l
}

func (h *hashVector) Clear() {
	panic("not implemented")
}

func (h *hashVector) Clone() clonable.Clonable {
	return NewHashVector(h.u.l)
}

func (h *hashVector) CopyTo(t IterableSSZ[common.Hash]) {
	tu := t.(*hashVector)
	h.u.CopyTo(tu.u)
}

func (h *hashVector) Static() bool {
	return true
}

func (h *hashVector) DecodeSSZ(buf []byte, version int) error {
	if len(buf) < h.Length()*length.Hash {
		return ssz.ErrBadDynamicLength
	}
	h.u.MerkleTree = nil
	copy(h.u.u, buf)
	return nil
}

func (h *hashVector) EncodeSSZ(buf []byte) ([]byte, error) {
	return h.u.EncodeSSZ(buf)
}

func (h *hashVector) EncodingSizeSSZ() int {
	return h.u.EncodingSizeSSZ()
}

func (h *hashVector) Get(index int) (out common.Hash) {
	return h.u.Get(index)
}

func (h *hashVector) Set(index int, newValue common.Hash) {
	h.u.Set(index, newValue)
}

func (h *hashVector) HashSSZ() ([32]byte, error) {
	return h.u.hashVectorSSZ()
}

func (h *hashVector) Range(fn func(int, common.Hash, int) bool) {
	h.u.Range(fn)
}

func (h *hashVector) Pop() common.Hash {
	panic("didnt ask, dont need it, go fuck yourself")
}
