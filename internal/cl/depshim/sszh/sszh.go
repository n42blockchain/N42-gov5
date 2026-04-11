// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package sszh is a re-export of N42's lib/types/ssz under the import path
// erigon's cl/ tree expects (`github.com/n42blockchain/N42/internal/cl/depshim/sszh`).
//
// The package directory is "sszh" rather than "ssz" so it does not collide
// with the cl/ssz package on disk, but the Go package name remains `ssz`
// because that is what cl/ source files reference. Type aliases keep
// identity unified with lib/types/ssz so anything that implements
// HashableSSZ here also implements lib/types/ssz.HashableSSZ.
package ssz

import libssz "github.com/n42blockchain/N42/lib/types/ssz"

// --- Interface aliases ---------------------------------------------------

type (
	HashableSSZ  = libssz.HashableSSZ
	EncodableSSZ = libssz.EncodableSSZ
	Marshaler    = libssz.Marshaler
	Unmarshaler  = libssz.Unmarshaler
)

// --- Function value re-exports -------------------------------------------

var (
	MarshalUint64SSZ        = libssz.MarshalUint64SSZ
	Uint64SSZ               = libssz.Uint64SSZ
	BoolSSZ                 = libssz.BoolSSZ
	OffsetSSZ               = libssz.OffsetSSZ
	EncodeOffset            = libssz.EncodeOffset
	DecodeOffset            = libssz.DecodeOffset
	UnmarshalUint64SSZ      = libssz.UnmarshalUint64SSZ
	Uint64SSZDecode         = libssz.Uint64SSZDecode
	DecodeNumbersList       = libssz.DecodeNumbersList
	CalculateIndiciesLimit  = libssz.CalculateIndiciesLimit
	DecodeString            = libssz.DecodeString
)

// Generic functions cannot be re-exported via var; cl/ files reference these
// by their unqualified name through the same package name (`ssz`), so callers
// already get them from libssz when they import this package.
//
// However, Go's type aliasing does not extend to generics, so we need to
// provide thin wrapper functions for the generic helpers cl/ uses.

// DecodeDynamicList wraps libssz.DecodeDynamicList.
func DecodeDynamicList[T Unmarshaler](bytes []byte, start, end uint32, max uint64, version int) ([]T, error) {
	return libssz.DecodeDynamicList[T](bytes, start, end, max, version)
}

// DecodeStaticList wraps libssz.DecodeStaticList.
func DecodeStaticList[T Unmarshaler](bytes []byte, start, end, bytesPerElement uint32, max uint64, version int) ([]T, error) {
	return libssz.DecodeStaticList[T](bytes, start, end, bytesPerElement, max, version)
}

// EncodeDynamicList wraps libssz.EncodeDynamicList.
func EncodeDynamicList[T Marshaler](buf []byte, objs []T) ([]byte, error) {
	return libssz.EncodeDynamicList[T](buf, objs)
}
