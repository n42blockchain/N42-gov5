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
// Miner subsystem configuration.
// MinerConfig holds the Etherbase coinbase, GasCeil target, default
// GasPrice floor and block Recommit interval used by the block
// builder when a consensus engine requests a new payload.

package conf

import (
	"math/big"
	"time"
)

type MinerConfig struct {
	Etherbase string
	GasCeil   uint64
	GasPrice  *big.Int
	Recommit  time.Duration
	// BlockIntervalMs throttles the wall-clock rate at which a leader seals
	// blocks to a fixed, drift-free interval (default 2000ms = 2s). The header
	// timestamp stays the deterministic parent.Time+period value; this only
	// paces real production. 0 disables the throttle — blocks are produced flat
	// out (bounded only by execution + consensus round-trips), which is the
	// "fast" mode used for benchmarking.
	BlockIntervalMs uint64
}
