// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// go:generate tool pin for the cmd/utils package.
// Uses the conventional `tools` build tag and blank-imports
// github.com/fjl/gencodec so `go mod tidy` keeps code-generation
// helpers in go.sum without shipping them in release binaries.

//go:build tools

package tools

import (
	// Tool imports for go:generate.
	_ "github.com/fjl/gencodec"
)
