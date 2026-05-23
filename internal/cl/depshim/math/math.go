// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Math unit for the math package.
// Numeric helper functions.
// Part of the n42el consensus-layer build.

//go:build n42el

// Package math re-exports the SafeAdd helper that cl/ uses from erigon's
// common/math. The implementation lives in N42's lib/common/math.

package math

import libmath "github.com/n42blockchain/N42/lib/common/math"

// SafeAdd returns x + y. The second return value is true on overflow.
var SafeAdd = libmath.SafeAdd
