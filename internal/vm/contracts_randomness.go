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

package vm

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

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
)

// Global block randomness, protected by RWMutex for thread-safe access.
var (
	globalRandomnessMu    sync.RWMutex
	globalBlockRandomness types.Hash
)

// SetBlockRandomness sets the global block randomness value.
// Called at the start of each block by the consensus layer.
func SetBlockRandomness(r types.Hash) {
	globalRandomnessMu.Lock()
	defer globalRandomnessMu.Unlock()
	globalBlockRandomness = r
}

// getBlockRandomness returns the current global block randomness.
func getBlockRandomness() types.Hash {
	globalRandomnessMu.RLock()
	defer globalRandomnessMu.RUnlock()
	return globalBlockRandomness
}

// randomnessBeacon implements the on-chain randomness precompile at address 0x0302.
//
// Inspired by Aptos on-chain randomness (AIP-41). Provides deterministic,
// per-block randomness derived from consensus-committed randomness values.
//
// Function selectors:
//
//	0x00: getRandom() -> bytes32                        — keccak256(blockRandomness)
//	0x01: getRandomInRange(max uint256) -> uint256      — hash % max
//	0x02: getRandomWithSeed(seed bytes32) -> bytes32    — keccak256(randomness || seed)
type randomnessBeacon struct{}

// RequiredGas returns the gas required to execute the randomness precompile.
func (c *randomnessBeacon) RequiredGas(input []byte) uint64 {
	if len(input) < 1 {
		return RandomnessGetRandomGas
	}
	switch input[0] {
	case rngGetRandom:
		return RandomnessGetRandomGas
	case rngGetRandomInRange:
		return RandomnessGetRandomInRangeGas
	case rngGetRandomWithSeed:
		return RandomnessGetRandomGas
	default:
		return RandomnessGetRandomGas
	}
}

// Run executes the randomness precompile.
func (c *randomnessBeacon) Run(input []byte) ([]byte, error) {
	if len(input) < 1 {
		return nil, errRandomnessInvalidInput
	}

	switch input[0] {
	case rngGetRandom:
		return c.runGetRandom()
	case rngGetRandomInRange:
		return c.runGetRandomInRange(input[1:])
	case rngGetRandomWithSeed:
		return c.runGetRandomWithSeed(input[1:])
	default:
		return nil, fmt.Errorf("%w: 0x%02x", errRandomnessUnknownOp, input[0])
	}
}

// runGetRandom handles selector 0x00.
// Output: keccak256(blockRandomness) [32]byte.
func (c *randomnessBeacon) runGetRandom() ([]byte, error) {
	r := getBlockRandomness()
	hash := crypto.Keccak256Hash(r[:])
	return hash[:], nil
}

// runGetRandomInRange handles selector 0x01.
// Input: max [32]byte (big-endian uint256).
// Output: hash % max [32]byte (big-endian uint256).
func (c *randomnessBeacon) runGetRandomInRange(data []byte) ([]byte, error) {
	if len(data) < 32 {
		return nil, fmt.Errorf("%w: getRandomInRange requires 32 bytes, got %d", errRandomnessInvalidInput, len(data))
	}

	max := new(big.Int).SetBytes(data[:32])
	if max.Sign() == 0 {
		return nil, errRandomnessZeroMax
	}

	r := getBlockRandomness()
	hash := crypto.Keccak256Hash(r[:])
	val := new(big.Int).SetBytes(hash[:])
	val.Mod(val, max)

	// Left-pad to 32 bytes.
	result := make([]byte, 32)
	valBytes := val.Bytes()
	copy(result[32-len(valBytes):], valBytes)
	return result, nil
}

// runGetRandomWithSeed handles selector 0x02.
// Input: seed [32]byte.
// Output: keccak256(randomness || seed) [32]byte.
func (c *randomnessBeacon) runGetRandomWithSeed(data []byte) ([]byte, error) {
	if len(data) < 32 {
		return nil, fmt.Errorf("%w: getRandomWithSeed requires 32 bytes, got %d", errRandomnessInvalidInput, len(data))
	}

	r := getBlockRandomness()
	combined := make([]byte, 64)
	copy(combined[:32], r[:])
	copy(combined[32:], data[:32])
	hash := crypto.Keccak256Hash(combined)
	return hash[:], nil
}
