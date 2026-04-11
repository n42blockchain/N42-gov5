// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package raw

import "errors"

var (
	// Error for missing validator
	ErrInvalidValidatorIndex = errors.New("invalid validator index")
)
