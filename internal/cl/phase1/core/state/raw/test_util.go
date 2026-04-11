// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Test util unit for the raw package.
// Exports helpers such as GetTestState.
// Part of the n42el consensus-layer build.

//go:build n42el

package raw

import (
	_ "embed"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/utils"
)

//go:embed testdata/state.ssz_snappy
var denebState []byte

func GetTestState() *BeaconState {
	state := New(&clparams.MainnetBeaconConfig)
	utils.DecodeSSZSnappy(state, denebState, int(clparams.DenebVersion))
	return state

}
