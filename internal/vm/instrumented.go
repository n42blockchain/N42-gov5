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
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// InstrumentedVM wraps an EVM with instrumentation for timing and metrics.
// This enables performance monitoring without modifying the core EVM.
//
// Usage:
//
//	evm := vm.NewEVM(...)
//	instrumented := vm.NewInstrumentedVM(evm, true)
//	// Use instrumented as a VMInterpreter
//	instrumented.LogStats()
type InstrumentedVM struct {
	inner   *EVM
	enabled bool

	// Call metrics
	callCount    uint64
	callTimeNs   uint64
	callMaxDepth uint64

	// Create metrics
	createCount  uint64
	createTimeNs uint64

	// Static call metrics
	staticCallCount  uint64
	staticCallTimeNs uint64

	// Delegate call metrics
	delegateCallCount  uint64
	delegateCallTimeNs uint64
}

// NewInstrumentedVM creates a new instrumented VM wrapper.
// Set enabled=false in production to minimize overhead.
func NewInstrumentedVM(inner *EVM, enabled bool) *InstrumentedVM {
	return &InstrumentedVM{
		inner:   inner,
		enabled: enabled,
	}
}

// =============================================================================
// VMCaller Interface Implementation
// =============================================================================

func (v *InstrumentedVM) Call(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int, bailout bool) (ret []byte, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.Call(caller, addr, input, gas, value, bailout)
	}

	start := time.Now()
	ret, leftOverGas, err = v.inner.Call(caller, addr, input, gas, value, bailout)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.callCount, 1)
	atomic.AddUint64(&v.callTimeNs, elapsed)

	// Track max depth
	depth := uint64(v.inner.interpreter.Depth())
	for {
		current := atomic.LoadUint64(&v.callMaxDepth)
		if depth <= current {
			break
		}
		if atomic.CompareAndSwapUint64(&v.callMaxDepth, current, depth) {
			break
		}
	}

	return ret, leftOverGas, err
}

func (v *InstrumentedVM) CallCode(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int) (ret []byte, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.CallCode(caller, addr, input, gas, value)
	}

	start := time.Now()
	ret, leftOverGas, err = v.inner.CallCode(caller, addr, input, gas, value)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.callCount, 1)
	atomic.AddUint64(&v.callTimeNs, elapsed)

	return ret, leftOverGas, err
}

func (v *InstrumentedVM) DelegateCall(caller ContractRef, addr types.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.DelegateCall(caller, addr, input, gas)
	}

	start := time.Now()
	ret, leftOverGas, err = v.inner.DelegateCall(caller, addr, input, gas)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.delegateCallCount, 1)
	atomic.AddUint64(&v.delegateCallTimeNs, elapsed)

	return ret, leftOverGas, err
}

func (v *InstrumentedVM) StaticCall(caller ContractRef, addr types.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.StaticCall(caller, addr, input, gas)
	}

	start := time.Now()
	ret, leftOverGas, err = v.inner.StaticCall(caller, addr, input, gas)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.staticCallCount, 1)
	atomic.AddUint64(&v.staticCallTimeNs, elapsed)

	return ret, leftOverGas, err
}

func (v *InstrumentedVM) Create(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int) (ret []byte, contractAddr types.Address, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.Create(caller, code, gas, endowment)
	}

	start := time.Now()
	ret, contractAddr, leftOverGas, err = v.inner.Create(caller, code, gas, endowment)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.createCount, 1)
	atomic.AddUint64(&v.createTimeNs, elapsed)

	return ret, contractAddr, leftOverGas, err
}

func (v *InstrumentedVM) Create2(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int, salt *uint256.Int) (ret []byte, contractAddr types.Address, leftOverGas uint64, err error) {
	if !v.enabled {
		return v.inner.Create2(caller, code, gas, endowment, salt)
	}

	start := time.Now()
	ret, contractAddr, leftOverGas, err = v.inner.Create2(caller, code, gas, endowment, salt)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&v.createCount, 1)
	atomic.AddUint64(&v.createTimeNs, elapsed)

	return ret, contractAddr, leftOverGas, err
}

// =============================================================================
// VMInterpreter Passthrough Methods
// =============================================================================

func (v *InstrumentedVM) ChainRules() *params.Rules          { return v.inner.ChainRules() }
func (v *InstrumentedVM) ChainConfig() *params.ChainConfig   { return v.inner.ChainConfig() }
func (v *InstrumentedVM) IntraBlockState() evmtypes.IntraBlockState { return v.inner.IntraBlockState() }
func (v *InstrumentedVM) Context() evmtypes.BlockContext     { return v.inner.Context() }
func (v *InstrumentedVM) TxContext() evmtypes.TxContext      { return v.inner.TxContext() }
func (v *InstrumentedVM) Config() Config                     { return v.inner.Config() }
func (v *InstrumentedVM) SetCallGasTemp(gas uint64)          { v.inner.SetCallGasTemp(gas) }
func (v *InstrumentedVM) CallGasTemp() uint64                { return v.inner.CallGasTemp() }
func (v *InstrumentedVM) Cancelled() bool                    { return v.inner.Cancelled() }
func (v *InstrumentedVM) Reset(txCtx evmtypes.TxContext, ibs evmtypes.IntraBlockState) {
	v.inner.Reset(txCtx, ibs)
}

// =============================================================================
// Statistics
// =============================================================================

// VMStats holds accumulated VM statistics.
type VMStats struct {
	CallCount        uint64
	CallTime         time.Duration
	CallMaxDepth     uint64
	CreateCount      uint64
	CreateTime       time.Duration
	StaticCallCount  uint64
	StaticCallTime   time.Duration
	DelegateCallCount uint64
	DelegateCallTime  time.Duration
}

// Stats returns the accumulated statistics.
func (v *InstrumentedVM) Stats() VMStats {
	return VMStats{
		CallCount:        atomic.LoadUint64(&v.callCount),
		CallTime:         time.Duration(atomic.LoadUint64(&v.callTimeNs)),
		CallMaxDepth:     atomic.LoadUint64(&v.callMaxDepth),
		CreateCount:      atomic.LoadUint64(&v.createCount),
		CreateTime:       time.Duration(atomic.LoadUint64(&v.createTimeNs)),
		StaticCallCount:  atomic.LoadUint64(&v.staticCallCount),
		StaticCallTime:   time.Duration(atomic.LoadUint64(&v.staticCallTimeNs)),
		DelegateCallCount: atomic.LoadUint64(&v.delegateCallCount),
		DelegateCallTime:  time.Duration(atomic.LoadUint64(&v.delegateCallTimeNs)),
	}
}

// LogStats logs the accumulated statistics at debug level.
func (v *InstrumentedVM) LogStats() {
	stats := v.Stats()
	log.Debug("VM stats",
		"calls", stats.CallCount,
		"call_time", stats.CallTime,
		"max_depth", stats.CallMaxDepth,
		"creates", stats.CreateCount,
		"create_time", stats.CreateTime,
		"static_calls", stats.StaticCallCount,
		"delegate_calls", stats.DelegateCallCount,
	)
}

// Reset clears all counters.
func (v *InstrumentedVM) ResetStats() {
	atomic.StoreUint64(&v.callCount, 0)
	atomic.StoreUint64(&v.callTimeNs, 0)
	atomic.StoreUint64(&v.callMaxDepth, 0)
	atomic.StoreUint64(&v.createCount, 0)
	atomic.StoreUint64(&v.createTimeNs, 0)
	atomic.StoreUint64(&v.staticCallCount, 0)
	atomic.StoreUint64(&v.staticCallTimeNs, 0)
	atomic.StoreUint64(&v.delegateCallCount, 0)
	atomic.StoreUint64(&v.delegateCallTimeNs, 0)
}

// TotalCalls returns the total number of all call types.
func (s VMStats) TotalCalls() uint64 {
	return s.CallCount + s.StaticCallCount + s.DelegateCallCount
}

// TotalTime returns total time spent in all operations.
func (s VMStats) TotalTime() time.Duration {
	return s.CallTime + s.CreateTime + s.StaticCallTime + s.DelegateCallTime
}

// Inner returns the underlying EVM instance.
func (v *InstrumentedVM) Inner() *EVM {
	return v.inner
}

// =============================================================================
// Compile-time interface compliance
// =============================================================================

var _ VMInterpreter = (*InstrumentedVM)(nil)

