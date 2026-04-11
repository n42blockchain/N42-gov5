// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Errors unit for the raw package.
// Part of the n42el consensus-layer build.
// Part of the n42el consensus-layer build.

//go:build n42el

package raw

import "errors"

var (
	// Error for missing validator
	ErrInvalidValidatorIndex = errors.New("invalid validator index")
)
