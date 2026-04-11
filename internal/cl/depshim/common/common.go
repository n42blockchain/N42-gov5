// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Common unit for the common package.
// Provides constructors NewUint64.
// Exports helpers such as Bytes2Hex and NewUint64.
// Common type shims used by CL code.

//go:build n42el

// Package common is a thin re-export layer over N42's lib/common. The cl/
// tree imports erigon's `github.com/n42blockchain/N42/internal/cl/depshim/common` for Hash,
// Address, fixed-size byte types, and a handful of helpers — every one of
// which already exists in lib/common because lib/common was forked from
// erigon. Re-exporting via Go type/value aliases keeps type identity
// consistent across the entire N42 + cl/ codebase: a depshim/common.Hash
// IS a lib/common.Hash, not a parallel copy.
//
// A few helpers (Bytes2Hex, NewUint64) that erigon's common package exposes
// but N42's lib/common does not are provided locally.
package common

import (
	"encoding/hex"

	libcommon "github.com/n42blockchain/N42/lib/common"
)

// --- Type aliases ---------------------------------------------------------

type (
	Hash    = libcommon.Hash
	Address = libcommon.Address
	Bytes4  = libcommon.Bytes4
	Bytes48 = libcommon.Bytes48
	Bytes64 = libcommon.Bytes64
	Bytes96 = libcommon.Bytes96
)

// --- Function value re-exports -------------------------------------------

var (
	BytesToHash    = libcommon.BytesToHash
	CastToHash     = libcommon.CastToHash
	BigToHash      = libcommon.BigToHash
	HexToHash      = libcommon.HexToHash
	BytesToAddress = libcommon.BytesToAddress
	HexToAddress   = libcommon.HexToAddress
	IsHexAddress   = libcommon.IsHexAddress
	ByteCount      = libcommon.ByteCount
	Copy           = libcommon.Copy
	Hex2Bytes      = libcommon.Hex2Bytes
	FromHex        = libcommon.FromHex
)

// --- Local helpers (not present in lib/common) ----------------------------

// Bytes2Hex returns the hex encoding of b without a 0x prefix. cl/ uses it
// in a few logging spots; lib/common does not provide it.
func Bytes2Hex(b []byte) string {
	return hex.EncodeToString(b)
}

// NewUint64 boxes a uint64 into a heap-allocated pointer. cl/ uses this for
// optional EL header fields like BlobGasUsed.
func NewUint64(v uint64) *uint64 {
	out := new(uint64)
	*out = v
	return out
}
