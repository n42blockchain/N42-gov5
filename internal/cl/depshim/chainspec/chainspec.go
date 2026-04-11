// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package chainspec re-exports the chain-ID constants the cl/ pure-type
// layer references from erigon/execution/chain/spec. Only the IDs are
// needed in Phase 2 — the full chain spec (genesis, fork heights, etc.) is
// vendored later when the phase1 stages land.
package chainspec

// Untyped so they convert implicitly to caller-defined network ID types.
const (
	MainnetChainID    = 1
	SepoliaChainID    = 11155111
	HoleskyChainID    = 17000
	HoodiChainID      = 560048
	GnosisChainID     = 100
	ChiadoChainID     = 10200
	BloatnetNetworkID = 1337
)
