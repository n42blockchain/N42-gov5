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

// =============================================================================
// Precompiles Initialization Helpers
// =============================================================================
//
// This file provides helper functions for precompiled contracts initialization.
//
// The actual initialization happens in contracts.go via init(), which is the
// standard Go pattern for populating address slices from contract maps.
// These helpers are provided for:
//   - Documentation of initialization behavior
//   - Testing utilities
//   - Explicit initialization verification

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// PrecompilesInitialized returns true if precompiled contract address slices
// have been initialized. This should always return true after package import.
func PrecompilesInitialized() bool {
	// Check that all address slices are populated
	return len(PrecompiledAddressesHomestead) > 0 &&
		len(PrecompiledAddressesByzantium) > 0 &&
		len(PrecompiledAddressesIstanbul) > 0 &&
		len(PrecompiledAddressesBerlin) > 0
}

// PrecompileCount returns the number of precompiled contracts for each fork.
func PrecompileCount() map[string]int {
	return map[string]int{
		"Homestead":      len(PrecompiledAddressesHomestead),
		"Byzantium":      len(PrecompiledAddressesByzantium),
		"Istanbul":       len(PrecompiledAddressesIstanbul),
		"IstanbulForBSC": len(PrecompiledAddressesIstanbulForBSC),
		"Berlin":         len(PrecompiledAddressesBerlin),
		"Nano":           len(PrecompiledAddressesNano),
		"Moran":          len(PrecompiledAddressesMoran),
	}
}

// GetPrecompiledAddresses returns the precompiled addresses for the given rules.
// This is a convenience wrapper around ActivePrecompiles.
func GetPrecompiledAddresses(rules *params.Rules) []types.Address {
	return ActivePrecompiles(rules)
}

// IsPrecompiled checks if an address is a precompiled contract for the given rules.
func IsPrecompiled(addr types.Address, rules *params.Rules) bool {
	precompiles := ActivePrecompiles(rules)
	for _, p := range precompiles {
		if p == addr {
			return true
		}
	}
	return false
}

// GetPrecompiledContract returns the precompiled contract at the given address
// for the specified rules, or nil if not found.
func GetPrecompiledContract(addr types.Address, rules *params.Rules) PrecompiledContract {
	var precompiles map[types.Address]PrecompiledContract
	switch {
	case rules.IsMoran:
		precompiles = PrecompiledContractsIsMoran
	case rules.IsNano:
		precompiles = PrecompiledContractsNano
	case rules.IsBerlin:
		precompiles = PrecompiledContractsBerlin
	case rules.IsIstanbul:
		precompiles = PrecompiledContractsIstanbul
	case rules.IsByzantium:
		precompiles = PrecompiledContractsByzantium
	default:
		precompiles = PrecompiledContractsHomestead
	}
	return precompiles[addr]
}

