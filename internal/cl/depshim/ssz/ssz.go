//go:build n42el

// Package ssz re-exports N42's lib/types/ssz API at the upstream
// import path (erigon's cl tree imports common/ssz, which under
// our rewrite becomes internal/cl/depshim/ssz).
//
// Forwards the full set of helpers + error sentinels that the
// wholesale-copied cl/ssz, cl/cltypes, cl/merkle_tree etc.
// reference.
package ssz

import libssz "github.com/n42blockchain/N42/lib/types/ssz"

type (
	EncodableSSZ = libssz.EncodableSSZ
	Marshaler    = libssz.Marshaler
	Unmarshaler  = libssz.Unmarshaler
	HashableSSZ  = libssz.HashableSSZ
)

var (
	ErrLowBufferSize    = libssz.ErrLowBufferSize
	ErrBadDynamicLength = libssz.ErrBadDynamicLength
	ErrBufferNotRounded = libssz.ErrBufferNotRounded
	ErrBadOffset        = libssz.ErrBadOffset

	Uint64SSZ           = libssz.Uint64SSZ
	MarshalUint64SSZ    = libssz.MarshalUint64SSZ
	UnmarshalUint64SSZ  = libssz.UnmarshalUint64SSZ
	Uint64SSZDecode     = libssz.Uint64SSZDecode

	OffsetSSZ           = libssz.OffsetSSZ
	DecodeOffset        = libssz.DecodeOffset
)

// Generic functions can't be assigned to vars; wrap them.
func EncodeDynamicList[T libssz.Marshaler](buf []byte, objs []T) ([]byte, error) {
	return libssz.EncodeDynamicList(buf, objs)
}

func DecodeStaticList[T libssz.Unmarshaler](bytes []byte, start, end, bytesPerElement uint32, max uint64, version int) ([]T, error) {
	return libssz.DecodeStaticList[T](bytes, start, end, bytesPerElement, max, version)
}

func DecodeDynamicList[T libssz.Unmarshaler](bytes []byte, start, end uint32, max uint64, version int) ([]T, error) {
	return libssz.DecodeDynamicList[T](bytes, start, end, max, version)
}
