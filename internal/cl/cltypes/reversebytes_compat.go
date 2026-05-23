// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reversebytes compat unit for the cltypes package.
// Beacon chain SSZ data structures used across phases.
// Part of the n42el consensus-layer build.

//go:build n42el

// erigon's cltypes uses uint256.Int.ReverseBytes, which only exists in
// uint256 ≥ v1.3. N42 is pinned to v1.2.3 (go.mod replace directive,
// required for MainnetGenesisHash determinism). This file provides the
// in-place 32-byte reversal that the patched call sites in eth1_block.go
// and eth1_header.go invoke instead.


package cltypes

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/utils"
)

func reverseBytes256(z, x *uint256.Int) {
	var h common.Hash
	x.WriteToSlice(h[:])
	utils.ReverseBytes(&h)
	z.SetBytes(h[:])
}
