// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Interfaces unit for the solid package.
// Defines the Uint64ListSSZ, Uint64VectorSSZ, HashListSSZ, and HashVectorSSZ
// types.
// Fixed-layout SSZ containers with in-place encoding.

//go:build n42el

package solid

import (
	"encoding/json"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

type IterableSSZ[T any] interface {
	Clear()
	CopyTo(IterableSSZ[T])
	Range(fn func(index int, value T, length int) bool)
	Get(index int) T
	Set(index int, v T)
	Length() int
	Cap() int
	Bytes() []byte
	Pop() T
	Append(v T)

	ssz2.Sized
	ssz.EncodableSSZ
	ssz.HashableSSZ
}

type Uint64ListSSZ interface {
	IterableSSZ[uint64]
	json.Marshaler
	json.Unmarshaler
}

type Uint64VectorSSZ interface {
	IterableSSZ[uint64]
	json.Marshaler
	json.Unmarshaler
}

type HashListSSZ interface {
	IterableSSZ[common.Hash]
	json.Marshaler
	json.Unmarshaler
}

type HashVectorSSZ interface {
	IterableSSZ[common.Hash]
	json.Marshaler
	json.Unmarshaler
}
