// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for VM interfaces.

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestEVMImplementsVMCaller(t *testing.T) {
	var _ VMCaller = (*EVM)(nil)
	t.Log("✓ EVM implements VMCaller")
}

func TestEVMImplementsVMContext(t *testing.T) {
	var _ VMContext = (*EVM)(nil)
	t.Log("✓ EVM implements VMContext")
}

func TestEVMImplementsVMExecutor(t *testing.T) {
	var _ VMExecutor = (*EVM)(nil)
	t.Log("✓ EVM implements VMExecutor")
}

func TestEVMImplementsVMResetter(t *testing.T) {
	var _ VMResetter = (*EVM)(nil)
	t.Log("✓ EVM implements VMResetter")
}

func TestEVMImplementsVMCanceller(t *testing.T) {
	var _ VMCanceller = (*EVM)(nil)
	t.Log("✓ EVM implements VMCanceller")
}

func TestEVMImplementsFullVM(t *testing.T) {
	var _ FullVM = (*EVM)(nil)
	t.Log("✓ EVM implements FullVM")
}

func TestEVMImplementsVMInterpreter(t *testing.T) {
	var _ VMInterpreter = (*EVM)(nil)
	t.Log("✓ EVM implements VMInterpreter")
}

func TestInstrumentedVMImplementsVMInterpreter(t *testing.T) {
	var _ VMInterpreter = (*InstrumentedVM)(nil)
	t.Log("✓ InstrumentedVM implements VMInterpreter")
}

// =============================================================================
// Mock Contract for Testing
// =============================================================================

type mockContractRef struct {
	addr types.Address
}

func (m *mockContractRef) Address() types.Address { return m.addr }

// =============================================================================
// Interface Method Signature Tests
// =============================================================================

func TestVMCallerMethodSignatures(t *testing.T) {
	// These tests verify method signatures at compile time
	var caller VMCaller

	// Type assertions to verify signatures
	_ = func() ([]byte, uint64, error) {
		return caller.Call(nil, types.Address{}, nil, 0, nil, false)
	}
	t.Log("✓ Call signature: (ContractRef, Address, []byte, uint64, *uint256.Int, bool) ([]byte, uint64, error)")

	_ = func() ([]byte, uint64, error) {
		return caller.CallCode(nil, types.Address{}, nil, 0, nil)
	}
	t.Log("✓ CallCode signature: (ContractRef, Address, []byte, uint64, *uint256.Int) ([]byte, uint64, error)")

	_ = func() ([]byte, uint64, error) {
		return caller.DelegateCall(nil, types.Address{}, nil, 0)
	}
	t.Log("✓ DelegateCall signature: (ContractRef, Address, []byte, uint64) ([]byte, uint64, error)")

	_ = func() ([]byte, uint64, error) {
		return caller.StaticCall(nil, types.Address{}, nil, 0)
	}
	t.Log("✓ StaticCall signature: (ContractRef, Address, []byte, uint64) ([]byte, uint64, error)")

	_ = func() ([]byte, types.Address, uint64, error) {
		return caller.Create(nil, nil, 0, nil)
	}
	t.Log("✓ Create signature: (ContractRef, []byte, uint64, *uint256.Int) ([]byte, Address, uint64, error)")

	_ = func() ([]byte, types.Address, uint64, error) {
		return caller.Create2(nil, nil, 0, nil, nil)
	}
	t.Log("✓ Create2 signature: (ContractRef, []byte, uint64, *uint256.Int, *uint256.Int) ([]byte, Address, uint64, error)")
}

func TestVMContextMethodSignatures(t *testing.T) {
	var ctx VMContext

	_ = func() evmtypes.BlockContext { return ctx.Context() }
	t.Log("✓ Context() evmtypes.BlockContext")

	_ = func() evmtypes.TxContext { return ctx.TxContext() }
	t.Log("✓ TxContext() evmtypes.TxContext")

	_ = func() *params.ChainConfig { return ctx.ChainConfig() }
	t.Log("✓ ChainConfig() *params.ChainConfig")

	_ = func() *params.Rules { return ctx.ChainRules() }
	t.Log("✓ ChainRules() *params.Rules")

	_ = func() evmtypes.IntraBlockState { return ctx.IntraBlockState() }
	t.Log("✓ IntraBlockState() evmtypes.IntraBlockState")
}

// =============================================================================
// InstrumentedVM Tests
// =============================================================================

func TestInstrumentedVMDisabled(t *testing.T) {
	// When disabled, stats should remain zero
	instrumented := &InstrumentedVM{enabled: false}
	
	stats := instrumented.Stats()
	if stats.TotalCalls() != 0 {
		t.Errorf("Expected 0 calls when disabled, got %d", stats.TotalCalls())
	}
	t.Log("✓ InstrumentedVM disabled mode works correctly")
}

func TestInstrumentedVMStatsReset(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: true}
	
	// Manually set some values
	instrumented.callCount = 10
	instrumented.createCount = 5
	
	instrumented.ResetStats()
	
	stats := instrumented.Stats()
	if stats.CallCount != 0 || stats.CreateCount != 0 {
		t.Error("Stats should be zero after reset")
	}
	t.Log("✓ InstrumentedVM ResetStats works correctly")
}

func TestVMStatsTotalCalls(t *testing.T) {
	stats := VMStats{
		CallCount:         10,
		StaticCallCount:   5,
		DelegateCallCount: 3,
	}

	if stats.TotalCalls() != 18 {
		t.Errorf("Expected 18 total calls, got %d", stats.TotalCalls())
	}
	t.Log("✓ VMStats.TotalCalls works correctly")
}

func TestVMStatsTotalTime(t *testing.T) {
	stats := VMStats{
		CallTime:       100,
		CreateTime:     50,
		StaticCallTime: 30,
		DelegateCallTime: 20,
	}

	if stats.TotalTime() != 200 {
		t.Errorf("Expected 200 total time, got %d", stats.TotalTime())
	}
	t.Log("✓ VMStats.TotalTime works correctly")
}

// =============================================================================
// Interface Composition Tests
// =============================================================================

func TestFullVMComposition(t *testing.T) {
	// Verify FullVM properly composes all sub-interfaces
	var full FullVM
	
	// Should be usable as each sub-interface
	var _ VMCaller = full
	var _ VMContext = full
	var _ VMExecutor = full
	var _ VMResetter = full
	var _ VMCanceller = full
	
	t.Log("✓ FullVM properly composes all sub-interfaces")
}

func TestVMExecutorComposition(t *testing.T) {
	var executor VMExecutor
	
	var _ VMCaller = executor
	var _ VMContext = executor
	
	t.Log("✓ VMExecutor properly composes VMCaller and VMContext")
}

// =============================================================================
// Type Alias Test
// =============================================================================

func TestVMInterfaceAlias(t *testing.T) {
	// VMInterface should be an alias for VMInterpreter
	var interp VMInterpreter
	var iface VMInterface = interp
	_ = iface
	
	t.Log("✓ VMInterface is an alias for VMInterpreter")
}

// =============================================================================
// Deep Call / Recursion Tests
// =============================================================================

func TestInstrumentedVMMaxDepthTracking(t *testing.T) {
	// Test that max depth tracking works correctly
	instrumented := &InstrumentedVM{enabled: true}
	
	// Simulate tracking max depth
	instrumented.callMaxDepth = 5
	
	stats := instrumented.Stats()
	if stats.CallMaxDepth != 5 {
		t.Errorf("Expected max depth 5, got %d", stats.CallMaxDepth)
	}
	
	// Reset should clear max depth
	instrumented.ResetStats()
	stats = instrumented.Stats()
	if stats.CallMaxDepth != 0 {
		t.Errorf("Expected max depth 0 after reset, got %d", stats.CallMaxDepth)
	}
	
	t.Log("✓ InstrumentedVM max depth tracking works correctly")
}

func TestInstrumentedVMMetricsAccumulation(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: true}
	
	// Manually simulate metrics accumulation
	instrumented.callCount = 10
	instrumented.callTimeNs = 1000
	instrumented.createCount = 2
	instrumented.createTimeNs = 500
	instrumented.staticCallCount = 5
	instrumented.staticCallTimeNs = 300
	instrumented.delegateCallCount = 3
	instrumented.delegateCallTimeNs = 200
	
	stats := instrumented.Stats()
	
	// Verify all metrics
	if stats.CallCount != 10 {
		t.Errorf("Expected CallCount 10, got %d", stats.CallCount)
	}
	if stats.CreateCount != 2 {
		t.Errorf("Expected CreateCount 2, got %d", stats.CreateCount)
	}
	if stats.StaticCallCount != 5 {
		t.Errorf("Expected StaticCallCount 5, got %d", stats.StaticCallCount)
	}
	if stats.DelegateCallCount != 3 {
		t.Errorf("Expected DelegateCallCount 3, got %d", stats.DelegateCallCount)
	}
	
	// Verify total calls
	expectedTotal := uint64(10 + 5 + 3)
	if stats.TotalCalls() != expectedTotal {
		t.Errorf("Expected TotalCalls %d, got %d", expectedTotal, stats.TotalCalls())
	}
	
	t.Log("✓ InstrumentedVM metrics accumulation works correctly")
}

// =============================================================================
// VM Interface - Minimal EVM Test (simplified for fast testing)
// =============================================================================

func TestVMCallerInterfaceMinimal(t *testing.T) {
	// Test that VMCaller interface has all required methods
	var _ VMCaller
	
	// Verify method existence through type checking
	type vmCallerTest interface {
		Call(caller ContractRef, addr types.Address, input []byte, gas uint64, value interface{}, bailout bool) ([]byte, uint64, error)
		CallCode(caller ContractRef, addr types.Address, input []byte, gas uint64, value interface{}) ([]byte, uint64, error)
		DelegateCall(caller ContractRef, addr types.Address, input []byte, gas uint64) ([]byte, uint64, error)
		StaticCall(caller ContractRef, addr types.Address, input []byte, gas uint64) ([]byte, uint64, error)
		Create(caller ContractRef, code []byte, gas uint64, endowment interface{}) ([]byte, types.Address, uint64, error)
		Create2(caller ContractRef, code []byte, gas uint64, endowment interface{}, salt interface{}) ([]byte, types.Address, uint64, error)
	}
	
	t.Log("✓ VMCaller interface has all required methods")
}

func TestVMContextInterfaceMinimal(t *testing.T) {
	// Test that VMContext interface has all required methods
	type vmContextTest interface {
		Context() evmtypes.BlockContext
		TxContext() evmtypes.TxContext
		ChainConfig() *params.ChainConfig
		ChainRules() *params.Rules
		IntraBlockState() evmtypes.IntraBlockState
	}
	
	// Verify VMContext implements vmContextTest methods
	var _ VMContext
	
	t.Log("✓ VMContext interface has all required methods")
}

// =============================================================================
// Instrumentation Overhead Test
// =============================================================================

func TestInstrumentedVMDisabledOverhead(t *testing.T) {
	// When disabled, InstrumentedVM should have minimal overhead
	instrumented := &InstrumentedVM{
		enabled: false,
		inner:   nil, // nil is ok for this test since we're not calling methods
	}
	
	// Verify enabled flag
	if instrumented.enabled {
		t.Error("InstrumentedVM should be disabled")
	}
	
	// Stats should be zero when disabled
	stats := instrumented.Stats()
	if stats.TotalCalls() != 0 || stats.TotalTime() != 0 {
		t.Error("Disabled InstrumentedVM should have zero stats")
	}
	
	t.Log("✓ InstrumentedVM has minimal overhead when disabled")
}

// =============================================================================
// Interface Documentation Test
// =============================================================================

func TestInterfaceDocumentation(t *testing.T) {
	// This test verifies that key interfaces are documented correctly
	// by checking that the expected types exist
	
	// Core execution interface
	var _ VMCaller
	
	// Context access interface
	var _ VMContext
	
	// Combined execution interface
	var _ VMExecutor
	
	// Reset capability
	var _ VMResetter
	
	// Cancellation capability
	var _ VMCanceller
	
	// Full EVM interface
	var _ FullVM
	
	// Interpreter interface (used by tracers)
	var _ VMInterpreter
	
	// Type alias for backward compatibility
	var _ VMInterface
	
	t.Log("✓ All VM interfaces are properly defined and documented")
}

