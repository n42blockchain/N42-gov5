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
// N42 on-chain randomness beacon precompile at 0x0302.
//
// The randomness source is the block environment's PrevRanDao — a value
// committed in the block header and derived by consensus (on the native
// chain: from the parent CommitQC's 2f+1 BLS aggregate signature, a
// threshold-VUF no single validator can predict). Because it comes from
// the header, every node — leader at build time, followers at import
// time, and any later replay — reads the IDENTICAL value, and historical
// blocks re-execute bit-exactly.
//
// The previous design read a process-local ring fed by whatever CommitQC
// this node most recently observed: leader and followers observed
// different values, restarted nodes held zero, and replay could never
// reproduce it — any transaction touching 0x0302 forked the state root.
// Never reintroduce execution-visible values that do not come from the
// block itself.
//
// Activation checklist (before setting RandomnessTime on any chain):
// headers MUST carry a consensus-derived PrevRandao — the precompile
// fails deterministically (identically on every node) when it is absent.

package vm

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
)

// ContextAwarePrecompile is a precompiled contract that reads
// consensus-committed block-environment values. Gas accounting uses the
// standard RequiredGas; only execution receives the context.
type ContextAwarePrecompile interface {
	PrecompiledContract
	RunWithContext(ctx *evmtypes.BlockContext, input []byte) ([]byte, error)
}

// RandomnessAddress is the on-chain randomness beacon precompile address (0x0302).
var RandomnessAddress = types.HexToAddress("0x0000000000000000000000000000000000000302")

// PrecompiledContractsRandomness contains the on-chain randomness precompile.
// N42-specific, independent of standard fork precompile sets.
var PrecompiledContractsRandomness = map[types.Address]PrecompiledContract{
	RandomnessAddress: &randomnessBeacon{},
}

// PrecompiledAddressesRandomness is populated in init().
var PrecompiledAddressesRandomness []types.Address

func init() {
	PrecompiledAddressesRandomness = collectAddresses(PrecompiledContractsRandomness)
}

// Randomness function selectors (first byte of input).
const (
	rngGetRandom         byte = 0x00 // getRandom() -> bytes32
	rngGetRandomInRange  byte = 0x01 // getRandomInRange(max uint256) -> uint256
	rngGetRandomWithSeed byte = 0x02 // getRandomWithSeed(seed bytes32) -> bytes32
)

// Gas costs for randomness precompile operations.
const (
	// RandomnessGetRandomGas is the cost for getRandom and getRandomWithSeed.
	RandomnessGetRandomGas uint64 = 100

	// RandomnessGetRandomInRangeGas is the cost for getRandomInRange (includes modular reduction).
	RandomnessGetRandomInRangeGas uint64 = 150
)

// Error sentinels for randomness precompile.
var (
	errRandomnessInvalidInput = errors.New("randomness: invalid input")
	errRandomnessUnknownOp    = errors.New("randomness: unknown selector")
	errRandomnessZeroMax      = errors.New("randomness: max must be > 0")
	errRandomnessUnavailable  = errors.New("randomness: block carries no consensus randomness")
)

// randomnessBeacon implements the on-chain randomness precompile at 0x0302.
//
// Function selectors:
//
//	0x00: getRandom() -> bytes32                        — keccak256(prevRandao)
//	0x01: getRandomInRange(max uint256) -> uint256      — hash % max
//	0x02: getRandomWithSeed(seed bytes32) -> bytes32    — keccak256(prevRandao || seed)
type randomnessBeacon struct{}

// RequiredGas returns the gas required to execute the randomness precompile.
func (c *randomnessBeacon) RequiredGas(input []byte) uint64 {
	if len(input) < 1 {
		return RandomnessGetRandomGas
	}
	switch input[0] {
	case rngGetRandomInRange:
		return RandomnessGetRandomInRangeGas
	default:
		return RandomnessGetRandomGas
	}
}

// Run satisfies PrecompiledContract but must never be dispatched — the
// beacon requires the block context (see RunWithContext).
func (c *randomnessBeacon) Run(input []byte) ([]byte, error) {
	return nil, errRandomnessUnavailable
}

// RunWithContext executes the randomness precompile against the block's
// consensus-committed randomness.
func (c *randomnessBeacon) RunWithContext(ctx *evmtypes.BlockContext, input []byte) ([]byte, error) {
	if len(input) < 1 {
		return nil, errRandomnessInvalidInput
	}
	if ctx == nil || ctx.PrevRanDao == nil {
		return nil, errRandomnessUnavailable
	}
	r := *ctx.PrevRanDao

	switch input[0] {
	case rngGetRandom:
		hash := crypto.Keccak256Hash(r[:])
		return hash[:], nil

	case rngGetRandomInRange:
		data := input[1:]
		if len(data) < 32 {
			return nil, fmt.Errorf("%w: getRandomInRange requires 32 bytes, got %d", errRandomnessInvalidInput, len(data))
		}
		max := new(big.Int).SetBytes(data[:32])
		if max.Sign() == 0 {
			return nil, errRandomnessZeroMax
		}
		hash := crypto.Keccak256Hash(r[:])
		val := new(big.Int).SetBytes(hash[:])
		val.Mod(val, max)
		result := make([]byte, 32)
		valBytes := val.Bytes()
		copy(result[32-len(valBytes):], valBytes)
		return result, nil

	case rngGetRandomWithSeed:
		data := input[1:]
		if len(data) < 32 {
			return nil, fmt.Errorf("%w: getRandomWithSeed requires 32 bytes, got %d", errRandomnessInvalidInput, len(data))
		}
		combined := make([]byte, 64)
		copy(combined[:32], r[:])
		copy(combined[32:], data[:32])
		hash := crypto.Keccak256Hash(combined)
		return hash[:], nil

	default:
		return nil, fmt.Errorf("%w: 0x%02x", errRandomnessUnknownOp, input[0])
	}
}
