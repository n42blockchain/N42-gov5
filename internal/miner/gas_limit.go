// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// CalcGasLimit: EIP-1559-compatible block gas limit adjustment. Moves
// the parent gas limit toward a desired target within
// params.GasLimitBoundDivisor per block and enforces the
// params.MinGasLimit floor. Used by the miner when sealing a new
// block header.

package miner

import (
	"os"

	"github.com/n42blockchain/N42/params"
)

// stressGasLimit is opt-in via N42_STRESS_GASLIMIT=1: it lets the miner jump the
// block gas limit straight to the desired ceiling (bypassing the per-block
// ±1/GasLimitBoundDivisor adjustment) so a throughput stress test can reach a high
// gas limit immediately. Only sound when ALL validators run with the same env
// (both this and the paired VerifyGaslimit relaxation must agree) — NOT for prod.
var stressGasLimit = os.Getenv("N42_STRESS_GASLIMIT") == "1"

func CalcGasLimit(parentGasLimit, desiredLimit uint64) uint64 {
	if desiredLimit < params.MinGasLimit {
		desiredLimit = params.MinGasLimit
	}
	if stressGasLimit {
		return desiredLimit // instant jump; paired with the stress VerifyGaslimit bypass
	}

	// Maximum per-block adjustment (minus 1 to prevent exact-match overshooting).
	// Safe: if parentGasLimit < GasLimitBoundDivisor the result is 0, avoiding underflow.
	delta := parentGasLimit / params.GasLimitBoundDivisor
	if delta > 0 {
		delta--
	}

	switch {
	case parentGasLimit < desiredLimit:
		limit := parentGasLimit + delta
		if limit > desiredLimit {
			limit = desiredLimit
		}
		return limit

	case parentGasLimit > desiredLimit:
		// Prevent underflow when delta exceeds parentGasLimit.
		if delta > parentGasLimit {
			return desiredLimit
		}
		limit := parentGasLimit - delta
		if limit < desiredLimit {
			limit = desiredLimit
		}
		return limit

	default:
		return parentGasLimit
	}
}
