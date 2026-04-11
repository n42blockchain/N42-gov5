// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Errors unit for the hexutil package.
// Defines the decError types.
// Hex encoding utilities for JSON marshalling.

//go:build n42el

package hexutil

import (
	"fmt"
	"math/bits"
)

// These errors are from go-ethereum in order to keep compatibility with geth error codes.
var (
	ErrEmptyString      = &decError{"empty hex string"}
	ErrSyntax           = &decError{"invalid hex string"}
	ErrMissingPrefix    = &decError{"hex string without 0x prefix"}
	ErrOddLength        = &decError{"hex string of odd length"}
	ErrEmptyNumber      = &decError{"hex string \"0x\""}
	ErrLeadingZero      = &decError{"hex number with leading zero digits"}
	ErrUint64Range      = &decError{"hex number > 64 bits"}
	ErrUintRange        = &decError{fmt.Sprintf("hex number > %d bits", bits.UintSize)}
	ErrUint16Range      = &decError{"hex number > 16 bits"}
	ErrBig256Range      = &decError{"hex number > 256 bits"}
	ErrTooBigHexString  = &decError{"hex string too long, want at most 32 bytes"}
	ErrHexStringInvalid = &decError{"hex string invalid"}
)

type decError struct{ msg string }

func (err decError) Error() string { return err.msg }
