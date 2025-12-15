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

// Package precompiles provides a registry-based approach for managing
// precompiled contracts, replacing the global map-based approach.
//
// Benefits:
//   - Eliminates global state and init() side effects
//   - Enables per-chain/per-block precompile configuration
//   - Improves testability via dependency injection
//   - Supports feature flags for rollback
//
// Usage:
//
//	registry := precompiles.NewRegistry(chainConfig, blockNumber)
//	evm := vm.NewEVMWithPrecompiles(ctx, state, config, registry)
package precompiles

import (
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// PrecompiledContract is re-exported from vm package for convenience.
type PrecompiledContract = vm.PrecompiledContract

// Registry manages precompiled contracts for a specific chain configuration.
// It is immutable after creation and safe for concurrent use.
type Registry struct {
	contracts map[types.Address]PrecompiledContract
	addresses []types.Address // Sorted list for ActivePrecompiles()
	rules     *params.Rules

	// Instrumentation (optional)
	enableMetrics bool
	lookupCount   uint64
	lookupTimeNs  uint64
	callCount     uint64
	callTimeNs    uint64
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithMetrics enables instrumentation metrics.
func WithMetrics(enabled bool) RegistryOption {
	return func(r *Registry) {
		r.enableMetrics = enabled
	}
}

// NewRegistry creates a new precompile registry based on chain rules.
// This replaces the global PrecompiledContractsXXX maps.
func NewRegistry(rules *params.Rules, opts ...RegistryOption) *Registry {
	r := &Registry{
		contracts: make(map[types.Address]PrecompiledContract),
		rules:     rules,
	}

	// Apply options
	for _, opt := range opts {
		opt(r)
	}

	// Register precompiles based on chain rules
	r.registerForRules(rules)

	return r
}

// registerForRules registers precompiles based on the active chain rules.
func (r *Registry) registerForRules(rules *params.Rules) {
	// Base precompiles (Homestead)
	r.register(1, NewEcrecover())
	r.register(2, NewSha256())
	r.register(3, NewRipemd160())
	r.register(4, NewDataCopy())

	// Byzantium additions
	if rules.IsByzantium {
		r.register(5, NewBigModExp(false))
		r.register(6, NewBn256Add(false))
		r.register(7, NewBn256ScalarMul(false))
		r.register(8, NewBn256Pairing(false))
	}

	// Istanbul additions
	if rules.IsIstanbul {
		r.register(5, NewBigModExp(false))
		r.register(6, NewBn256Add(true))      // Istanbul version
		r.register(7, NewBn256ScalarMul(true)) // Istanbul version
		r.register(8, NewBn256Pairing(true))   // Istanbul version
		r.register(9, NewBlake2F())
	}

	// Berlin changes (EIP-2565 modexp repricing)
	if rules.IsBerlin {
		r.register(5, NewBigModExp(true)) // EIP-2565 enabled
	}

	// Prague additions (EIP-7212/EIP-7951: P-256 precompile)
	if rules.IsPrague {
		// P-256 precompile at address 0x0000...0100
		p256Addr := types.BytesToAddress([]byte{0x01, 0x00})
		r.registerAt(p256Addr, NewP256Verify())
	}

	// Build sorted address list
	r.addresses = make([]types.Address, 0, len(r.contracts))
	for addr := range r.contracts {
		r.addresses = append(r.addresses, addr)
	}
}

// register adds a precompile at the given address index (1-255).
func (r *Registry) register(index byte, contract PrecompiledContract) {
	addr := types.BytesToAddress([]byte{index})
	r.contracts[addr] = contract
}

// registerAt adds a precompile at an arbitrary address.
func (r *Registry) registerAt(addr types.Address, contract PrecompiledContract) {
	r.contracts[addr] = contract
}

// Lookup returns the precompiled contract at the given address.
// Returns nil, false if no precompile exists at that address.
func (r *Registry) Lookup(addr types.Address) (PrecompiledContract, bool) {
	if r.enableMetrics {
		start := time.Now()
		defer func() {
			atomic.AddUint64(&r.lookupCount, 1)
			atomic.AddUint64(&r.lookupTimeNs, uint64(time.Since(start).Nanoseconds()))
		}()
	}

	p, ok := r.contracts[addr]
	return p, ok
}

// Run executes a precompiled contract with instrumentation.
func (r *Registry) Run(addr types.Address, input []byte, suppliedGas uint64) ([]byte, uint64, error) {
	p, ok := r.contracts[addr]
	if !ok {
		return nil, suppliedGas, nil
	}

	if r.enableMetrics {
		start := time.Now()
		defer func() {
			atomic.AddUint64(&r.callCount, 1)
			atomic.AddUint64(&r.callTimeNs, uint64(time.Since(start).Nanoseconds()))
		}()
	}

	gasCost := p.RequiredGas(input)
	if gasCost > suppliedGas {
		return nil, 0, ErrOutOfGas
	}

	output, err := p.Run(input)
	return output, suppliedGas - gasCost, err
}

// ActivePrecompiles returns the list of active precompile addresses.
func (r *Registry) ActivePrecompiles() []types.Address {
	return r.addresses
}

// Has returns true if a precompile exists at the given address.
func (r *Registry) Has(addr types.Address) bool {
	_, ok := r.contracts[addr]
	return ok
}

// Stats returns instrumentation statistics.
func (r *Registry) Stats() RegistryStats {
	return RegistryStats{
		LookupCount:  atomic.LoadUint64(&r.lookupCount),
		LookupTimeNs: atomic.LoadUint64(&r.lookupTimeNs),
		CallCount:    atomic.LoadUint64(&r.callCount),
		CallTimeNs:   atomic.LoadUint64(&r.callTimeNs),
	}
}

// LogStats logs the accumulated statistics.
func (r *Registry) LogStats() {
	stats := r.Stats()
	log.Debug("Precompile registry stats",
		"lookups", stats.LookupCount,
		"lookup_time", time.Duration(stats.LookupTimeNs),
		"calls", stats.CallCount,
		"call_time", time.Duration(stats.CallTimeNs),
	)
}

// RegistryStats holds accumulated statistics.
type RegistryStats struct {
	LookupCount  uint64
	LookupTimeNs uint64
	CallCount    uint64
	CallTimeNs   uint64
}

// =============================================================================
// Legacy Compatibility
// =============================================================================

// FromLegacyMap creates a Registry from a legacy precompile map.
// This provides backward compatibility during migration.
func FromLegacyMap(contracts map[types.Address]PrecompiledContract, rules *params.Rules) *Registry {
	r := &Registry{
		contracts: make(map[types.Address]PrecompiledContract, len(contracts)),
		rules:     rules,
	}

	for addr, contract := range contracts {
		r.contracts[addr] = contract
	}

	r.addresses = make([]types.Address, 0, len(r.contracts))
	for addr := range r.contracts {
		r.addresses = append(r.addresses, addr)
	}

	return r
}

// =============================================================================
// Errors
// =============================================================================

// ErrOutOfGas is returned when gas is insufficient for precompile execution.
var ErrOutOfGas = &outOfGasError{}

type outOfGasError struct{}

func (e *outOfGasError) Error() string { return "out of gas" }

// =============================================================================
// Interface compliance
// =============================================================================

// Verify Registry implements vm.PrecompileRegistry interface
// Note: This is checked at compile time when used with EVM

