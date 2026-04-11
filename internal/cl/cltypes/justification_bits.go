// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Justification bits unit for the cltypes package.
// Declares the JustificationBits type aliases.
// Exports helpers such as Clone, Byte, DecodeSSZ, and EncodeSSZ.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"encoding/json"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

var _ ssz2.SizedObjectSSZ = (*JustificationBits)(nil)

const JustificationBitsLength = 4

type JustificationBits [JustificationBitsLength]bool // Bit vector of size 4

func (j JustificationBits) Clone() clonable.Clonable {
	return JustificationBits{}
}
func (j JustificationBits) Byte() (out byte) {
	for i, bit := range j {
		if !bit {
			continue
		}
		out += byte(utils.PowerOf2(uint64(i)))
	}
	return
}

func (j *JustificationBits) DecodeSSZ(b []byte, _ int) error {
	if len(b) < 1 {
		return ssz.ErrLowBufferSize
	}
	j[0] = b[0]&1 > 0
	j[1] = b[0]&2 > 0
	j[2] = b[0]&4 > 0
	j[3] = b[0]&8 > 0
	return nil
}

func (j JustificationBits) EncodeSSZ(buf []byte) ([]byte, error) {
	return append(buf, j.Byte()), nil
}

func (JustificationBits) EncodingSizeSSZ() int {
	return 1
}

func (JustificationBits) Static() bool {
	return true
}

func (j *JustificationBits) HashSSZ() (out [32]byte, err error) {
	out[0] = j.Byte()
	return
}

// CheckRange checks if bits in certain range are all enabled.
func (j JustificationBits) CheckRange(start int, end int) bool {
	checkBits := j[start:end]
	for _, bit := range checkBits {
		if !bit {
			return false
		}
	}
	return true
}

func (j JustificationBits) Copy() JustificationBits {
	return JustificationBits{j[0], j[1], j[2], j[3]}
}

func (j JustificationBits) MarshalJSON() ([]byte, error) {
	enc, err := j.EncodeSSZ(nil)
	if err != nil {
		return nil, err
	}
	return json.Marshal(hexutil.Bytes(enc))
}

func (j *JustificationBits) UnmarshalJSON(input []byte) error {
	var hex hexutil.Bytes
	if err := json.Unmarshal(input, &hex); err != nil {
		return err
	}
	return j.DecodeSSZ(hex, 0)
}
