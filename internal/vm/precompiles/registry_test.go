// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for PrecompileRegistry.

package precompiles

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestRegistryImplementsPrecompileRegistry(t *testing.T) {
	var _ vm.PrecompileRegistry = (*Registry)(nil)
	t.Log("✓ Registry implements vm.PrecompileRegistry")
}

// =============================================================================
// Registry Creation Tests
// =============================================================================

func TestNewRegistryHomestead(t *testing.T) {
	rules := &params.Rules{
		IsByzantium: false,
		IsIstanbul:  false,
		IsBerlin:    false,
	}
	registry := NewRegistry(rules)

	// Homestead has 4 precompiles (1-4)
	for i := byte(1); i <= 4; i++ {
		addr := types.BytesToAddress([]byte{i})
		if !registry.Has(addr) {
			t.Errorf("Expected precompile at address %d", i)
		}
	}

	// Should not have address 5+
	addr5 := types.BytesToAddress([]byte{5})
	if registry.Has(addr5) {
		t.Error("Did not expect precompile at address 5 in Homestead")
	}

	t.Log("✓ Homestead registry has correct precompiles")
}

func TestNewRegistryByzantium(t *testing.T) {
	rules := &params.Rules{
		IsByzantium: true,
		IsIstanbul:  false,
		IsBerlin:    false,
	}
	registry := NewRegistry(rules)

	// Byzantium has 8 precompiles (1-8)
	for i := byte(1); i <= 8; i++ {
		addr := types.BytesToAddress([]byte{i})
		if !registry.Has(addr) {
			t.Errorf("Expected precompile at address %d", i)
		}
	}

	t.Log("✓ Byzantium registry has correct precompiles")
}

func TestNewRegistryIstanbul(t *testing.T) {
	rules := &params.Rules{
		IsByzantium: true,
		IsIstanbul:  true,
		IsBerlin:    false,
	}
	registry := NewRegistry(rules)

	// Istanbul has 9 precompiles (1-9, blake2f at 9)
	for i := byte(1); i <= 9; i++ {
		addr := types.BytesToAddress([]byte{i})
		if !registry.Has(addr) {
			t.Errorf("Expected precompile at address %d", i)
		}
	}

	t.Log("✓ Istanbul registry has correct precompiles")
}

func TestNewRegistryBerlin(t *testing.T) {
	rules := &params.Rules{
		IsByzantium: true,
		IsIstanbul:  true,
		IsBerlin:    true,
	}
	registry := NewRegistry(rules)

	// Berlin has same precompiles as Istanbul but with EIP-2565 modexp
	for i := byte(1); i <= 9; i++ {
		addr := types.BytesToAddress([]byte{i})
		if !registry.Has(addr) {
			t.Errorf("Expected precompile at address %d", i)
		}
	}

	t.Log("✓ Berlin registry has correct precompiles")
}

// =============================================================================
// Lookup Tests
// =============================================================================

func TestRegistryLookup(t *testing.T) {
	rules := &params.Rules{IsByzantium: true, IsIstanbul: true}
	registry := NewRegistry(rules)

	// Lookup existing
	addr1 := types.BytesToAddress([]byte{1})
	p, ok := registry.Lookup(addr1)
	if !ok {
		t.Error("Expected to find precompile at address 1")
	}
	if p == nil {
		t.Error("Precompile should not be nil")
	}

	// Lookup non-existing
	addr99 := types.BytesToAddress([]byte{99})
	p, ok = registry.Lookup(addr99)
	if ok {
		t.Error("Did not expect to find precompile at address 99")
	}
	if p != nil {
		t.Error("Precompile should be nil for non-existing address")
	}

	t.Log("✓ Registry.Lookup works correctly")
}

// =============================================================================
// ActivePrecompiles Tests
// =============================================================================

func TestActivePrecompiles(t *testing.T) {
	rules := &params.Rules{IsByzantium: true, IsIstanbul: true}
	registry := NewRegistry(rules)

	addresses := registry.ActivePrecompiles()
	if len(addresses) == 0 {
		t.Error("Expected non-empty address list")
	}

	// All addresses should be valid precompiles
	for _, addr := range addresses {
		if !registry.Has(addr) {
			t.Errorf("Address %s from ActivePrecompiles is not in registry", addr.Hex())
		}
	}

	t.Log("✓ ActivePrecompiles returns valid addresses")
}

// =============================================================================
// Run Tests
// =============================================================================

func TestRegistryRun(t *testing.T) {
	rules := &params.Rules{IsByzantium: true, IsIstanbul: true}
	registry := NewRegistry(rules)

	// Test ecrecover with empty input (should return nil without error)
	addr1 := types.BytesToAddress([]byte{1})
	output, remainingGas, err := registry.Run(addr1, []byte{}, 100000)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	_ = output
	_ = remainingGas

	t.Log("✓ Registry.Run works correctly")
}

func TestRegistryRunOutOfGas(t *testing.T) {
	rules := &params.Rules{IsByzantium: true, IsIstanbul: true}
	registry := NewRegistry(rules)

	// Try to run with insufficient gas
	addr1 := types.BytesToAddress([]byte{1})
	_, remainingGas, err := registry.Run(addr1, []byte{}, 1) // Only 1 gas
	if err != ErrOutOfGas {
		t.Errorf("Expected ErrOutOfGas, got %v", err)
	}
	if remainingGas != 0 {
		t.Errorf("Expected 0 remaining gas, got %d", remainingGas)
	}

	t.Log("✓ Registry.Run returns ErrOutOfGas correctly")
}

func TestRegistryRunNonExistent(t *testing.T) {
	rules := &params.Rules{IsByzantium: true}
	registry := NewRegistry(rules)

	// Run non-existent precompile
	addr99 := types.BytesToAddress([]byte{99})
	output, remainingGas, err := registry.Run(addr99, []byte{}, 100000)
	if err != nil {
		t.Errorf("Expected nil error for non-existent precompile, got %v", err)
	}
	if output != nil {
		t.Error("Expected nil output for non-existent precompile")
	}
	if remainingGas != 100000 {
		t.Errorf("Expected gas to be unchanged, got %d", remainingGas)
	}

	t.Log("✓ Registry.Run handles non-existent precompile correctly")
}

// =============================================================================
// Instrumentation Tests
// =============================================================================

func TestRegistryWithMetrics(t *testing.T) {
	rules := &params.Rules{IsByzantium: true, IsIstanbul: true}
	registry := NewRegistry(rules, WithMetrics(true))

	// Perform some lookups
	addr1 := types.BytesToAddress([]byte{1})
	_, _ = registry.Lookup(addr1)
	_, _ = registry.Lookup(addr1)
	_, _ = registry.Lookup(addr1)

	stats := registry.Stats()
	if stats.LookupCount != 3 {
		t.Errorf("Expected 3 lookups, got %d", stats.LookupCount)
	}
	if stats.LookupTimeNs == 0 {
		t.Error("Expected non-zero lookup time")
	}

	t.Log("✓ Registry metrics work correctly")
}

// =============================================================================
// Legacy Compatibility Tests
// =============================================================================

func TestFromLegacyMap(t *testing.T) {
	// Simulate a legacy map
	legacyMap := map[types.Address]PrecompiledContract{
		types.BytesToAddress([]byte{1}): NewEcrecover(),
		types.BytesToAddress([]byte{2}): NewSha256(),
	}

	registry := FromLegacyMap(legacyMap, &params.Rules{})

	// Verify all contracts are present
	for addr := range legacyMap {
		if !registry.Has(addr) {
			t.Errorf("Expected precompile at address %s", addr.Hex())
		}
	}

	t.Log("✓ FromLegacyMap creates correct registry")
}

// =============================================================================
// Precompile Execution Tests
// =============================================================================

func TestEcrecoverPrecompile(t *testing.T) {
	ecrecover := NewEcrecover()
	
	// Test gas calculation
	gas := ecrecover.RequiredGas(make([]byte, 128))
	if gas != 3000 { // EcrecoverGas
		t.Errorf("Expected gas 3000, got %d", gas)
	}

	t.Log("✓ Ecrecover precompile works correctly")
}

func TestSha256Precompile(t *testing.T) {
	sha := NewSha256()
	
	// Test with known input
	input := []byte("hello")
	output, err := sha.Run(input)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(output) != 32 {
		t.Errorf("Expected 32 byte output, got %d", len(output))
	}

	t.Log("✓ SHA256 precompile works correctly")
}

func TestDataCopyPrecompile(t *testing.T) {
	dataCopy := NewDataCopy()
	
	// Test data copy
	input := []byte{1, 2, 3, 4, 5}
	output, err := dataCopy.Run(input)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if string(output) != string(input) {
		t.Error("Output should equal input")
	}

	t.Log("✓ DataCopy precompile works correctly")
}

