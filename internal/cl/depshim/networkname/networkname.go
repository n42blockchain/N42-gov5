// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Networkname unit for the networkname package.
// Network name constants.
// Part of the n42el consensus-layer build.

//go:build n42el

// Package networkname re-exports the network-name string constants the cl/
// pure-type layer uses. The values must match erigon's
// execution/chain/networkname package exactly so that beacon network
// presets resolve to the same chain configuration.
package networkname

const (
	Mainnet  = "mainnet"
	Sepolia  = "sepolia"
	Holesky  = "holesky"
	Hoodi    = "hoodi"
	Gnosis   = "gnosis"
	Chiado   = "chiado"
	Bloatnet = "bloatnet"
)
