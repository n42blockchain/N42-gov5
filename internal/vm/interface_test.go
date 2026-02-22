// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for VM interfaces.

package vm

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestInterfaceCompliance(t *testing.T) {
	// Verify that EVM satisfies all required interfaces at compile time.
	var _ VMCaller = (*EVM)(nil)
	var _ VMContext = (*EVM)(nil)
	var _ VMExecutor = (*EVM)(nil)
	var _ VMResetter = (*EVM)(nil)
	var _ VMCanceller = (*EVM)(nil)
	var _ FullVM = (*EVM)(nil)
	var _ VMInterpreter = (*EVM)(nil)

	// InstrumentedVM also satisfies VMInterpreter.
	var _ VMInterpreter = (*InstrumentedVM)(nil)

	// VMInterface is an alias for VMInterpreter.
	var interp VMInterpreter
	var _ VMInterface = interp
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
	var caller VMCaller

	_ = func() ([]byte, uint64, error) {
		return caller.Call(nil, types.Address{}, nil, 0, nil, false)
	}
	_ = func() ([]byte, uint64, error) {
		return caller.CallCode(nil, types.Address{}, nil, 0, nil)
	}
	_ = func() ([]byte, uint64, error) {
		return caller.DelegateCall(nil, types.Address{}, nil, 0)
	}
	_ = func() ([]byte, uint64, error) {
		return caller.StaticCall(nil, types.Address{}, nil, 0)
	}
	_ = func() ([]byte, types.Address, uint64, error) {
		return caller.Create(nil, nil, 0, nil)
	}
	_ = func() ([]byte, types.Address, uint64, error) {
		return caller.Create2(nil, nil, 0, nil, nil)
	}
}

func TestVMContextMethodSignatures(t *testing.T) {
	var ctx VMContext

	_ = func() evmtypes.BlockContext { return ctx.Context() }
	_ = func() evmtypes.TxContext { return ctx.TxContext() }
	_ = func() *params.ChainConfig { return ctx.ChainConfig() }
	_ = func() *params.Rules { return ctx.ChainRules() }
	_ = func() evmtypes.IntraBlockState { return ctx.IntraBlockState() }
}

// =============================================================================
// Interface Composition Tests
// =============================================================================

func TestInterfaceComposition(t *testing.T) {
	// FullVM properly composes all sub-interfaces.
	var full FullVM
	var _ VMCaller = full
	var _ VMContext = full
	var _ VMExecutor = full
	var _ VMResetter = full
	var _ VMCanceller = full

	// VMExecutor properly composes VMCaller and VMContext.
	var executor VMExecutor
	var _ VMCaller = executor
	var _ VMContext = executor
}

// =============================================================================
// InstrumentedVM Tests
// =============================================================================

func TestInstrumentedVMDisabled(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: false}

	stats := instrumented.Stats()
	if stats.TotalCalls() != 0 {
		t.Errorf("TotalCalls() = %d when disabled, want 0", stats.TotalCalls())
	}
	if stats.TotalTime() != 0 {
		t.Errorf("TotalTime() = %d when disabled, want 0", stats.TotalTime())
	}
}

func TestInstrumentedVMStatsReset(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: true}
	instrumented.callCount = 10
	instrumented.createCount = 5

	instrumented.ResetStats()

	stats := instrumented.Stats()
	if stats.CallCount != 0 || stats.CreateCount != 0 {
		t.Errorf("after ResetStats: CallCount=%d, CreateCount=%d, want 0, 0",
			stats.CallCount, stats.CreateCount)
	}
}

func TestInstrumentedVMMetricsAccumulation(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: true}
	instrumented.callCount = 10
	instrumented.callTimeNs = 1000
	instrumented.createCount = 2
	instrumented.createTimeNs = 500
	instrumented.staticCallCount = 5
	instrumented.staticCallTimeNs = 300
	instrumented.delegateCallCount = 3
	instrumented.delegateCallTimeNs = 200
	instrumented.callMaxDepth = 5

	stats := instrumented.Stats()

	if stats.CallCount != 10 {
		t.Errorf("CallCount = %d, want 10", stats.CallCount)
	}
	if stats.CreateCount != 2 {
		t.Errorf("CreateCount = %d, want 2", stats.CreateCount)
	}
	if stats.StaticCallCount != 5 {
		t.Errorf("StaticCallCount = %d, want 5", stats.StaticCallCount)
	}
	if stats.DelegateCallCount != 3 {
		t.Errorf("DelegateCallCount = %d, want 3", stats.DelegateCallCount)
	}
	if stats.CallMaxDepth != 5 {
		t.Errorf("CallMaxDepth = %d, want 5", stats.CallMaxDepth)
	}

	wantTotalCalls := uint64(10 + 5 + 3)
	if stats.TotalCalls() != wantTotalCalls {
		t.Errorf("TotalCalls() = %d, want %d", stats.TotalCalls(), wantTotalCalls)
	}

	wantTotalTime := time.Duration(1000 + 500 + 300 + 200)
	if stats.TotalTime() != wantTotalTime {
		t.Errorf("TotalTime() = %v, want %v", stats.TotalTime(), wantTotalTime)
	}
}

func TestInstrumentedVMMaxDepthReset(t *testing.T) {
	instrumented := &InstrumentedVM{enabled: true}
	instrumented.callMaxDepth = 5

	instrumented.ResetStats()

	stats := instrumented.Stats()
	if stats.CallMaxDepth != 0 {
		t.Errorf("CallMaxDepth after reset = %d, want 0", stats.CallMaxDepth)
	}
}
