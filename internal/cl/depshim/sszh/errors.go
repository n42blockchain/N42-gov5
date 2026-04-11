// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package ssz

import libssz "github.com/n42blockchain/N42/lib/types/ssz"

// Error variables re-exported from lib/types/ssz so cl/ files can compare
// error values across the seam.
var (
	ErrLowBufferSize     = libssz.ErrLowBufferSize
	ErrBadDynamicLength  = libssz.ErrBadDynamicLength
	ErrBufferNotRounded  = libssz.ErrBufferNotRounded
	ErrBadOffset         = libssz.ErrBadOffset
)
