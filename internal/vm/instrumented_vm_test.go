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
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// TestVMTInstrumentedVMCallFamily drives every call/create variant through
// InstrumentedVM with instrumentation enabled, then checks the accumulated
// stats and plumbing passthroughs.
func TestVMTInstrumentedVMCallFamily(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11ec")
	callee := types.HexToAddress("0x00000000000000000000000000000000feed04")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{byte(STOP)})
	ibs.PrepareAccessList(caller, &callee, nil, nil)

	iv := NewInstrumentedVM(evm, true)

	if _, _, err := iv.Call(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if _, _, err := iv.CallCode(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(0)); err != nil {
		t.Fatalf("CallCode failed: %v", err)
	}
	// DelegateCall's underlying evm.call(DELEGATECALL, ...) type-asserts its
	// caller to *Contract (AsDelegate), so a bare AccountRef panics here —
	// build a real Contract frame instead, the way a nested DELEGATECALL
	// opcode would.
	delegateFrame := NewContract(AccountRef(caller), AccountRef(callee), uint256.NewInt(0), 100_000, false)
	if _, _, err := iv.DelegateCall(delegateFrame, callee, nil, 100_000); err != nil {
		t.Fatalf("DelegateCall failed: %v", err)
	}
	if _, _, err := iv.StaticCall(AccountRef(caller), callee, nil, 100_000); err != nil {
		t.Fatalf("StaticCall failed: %v", err)
	}
	if _, _, _, err := iv.Create(AccountRef(caller), []byte{byte(STOP)}, 100_000, uint256.NewInt(0)); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if _, _, _, err := iv.Create2(AccountRef(caller), []byte{byte(STOP)}, 100_000, uint256.NewInt(0), uint256.NewInt(1)); err != nil {
		t.Fatalf("Create2 failed: %v", err)
	}

	stats := iv.Stats()
	if stats.CallCount != 2 { // Call + CallCode both bump callCount
		t.Fatalf("expected callCount=2, got %d", stats.CallCount)
	}
	if stats.StaticCallCount != 1 {
		t.Fatalf("expected staticCallCount=1, got %d", stats.StaticCallCount)
	}
	if stats.DelegateCallCount != 1 {
		t.Fatalf("expected delegateCallCount=1, got %d", stats.DelegateCallCount)
	}
	if stats.CreateCount != 2 {
		t.Fatalf("expected createCount=2, got %d", stats.CreateCount)
	}
	if stats.TotalCalls() != stats.CallCount+stats.StaticCallCount+stats.DelegateCallCount {
		t.Fatalf("TotalCalls() formula mismatch")
	}
	_ = stats.TotalTime()

	iv.LogStats()
	iv.ResetStats()
	resetStats := iv.Stats()
	if resetStats.CallCount != 0 || resetStats.CreateCount != 0 {
		t.Fatalf("expected ResetStats to zero all counters, got %+v", resetStats)
	}

	// Passthroughs.
	if iv.ChainRules() == nil {
		t.Fatalf("expected non-nil ChainRules")
	}
	if iv.ChainConfig() == nil {
		t.Fatalf("expected non-nil ChainConfig")
	}
	if iv.IntraBlockState() == nil {
		t.Fatalf("expected non-nil IntraBlockState")
	}
	_ = iv.Context()
	_ = iv.TxContext()
	_ = iv.Config()
	iv.SetCallGasTemp(42)
	if iv.CallGasTemp() != 42 {
		t.Fatalf("expected CallGasTemp passthrough to round-trip 42, got %d", iv.CallGasTemp())
	}
	if iv.Cancelled() {
		t.Fatalf("expected Cancelled() to be false")
	}
	if iv.Inner() != evm {
		t.Fatalf("expected Inner() to return the wrapped EVM")
	}

	iv.Reset(evm.TxContext(), ibs)
}

// TestVMTInstrumentedVMDisabledPassthrough verifies that with enabled=false
// every call/create variant short-circuits straight to the inner EVM
// without touching any counters.
func TestVMTInstrumentedVMDisabledPassthrough(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11ed")
	callee := types.HexToAddress("0x00000000000000000000000000000000feed05")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{byte(STOP)})
	ibs.PrepareAccessList(caller, &callee, nil, nil)

	iv := NewInstrumentedVM(evm, false)

	if _, _, err := iv.Call(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(0), false); err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if _, _, err := iv.CallCode(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(0)); err != nil {
		t.Fatalf("CallCode failed: %v", err)
	}
	// DelegateCall's underlying evm.call(DELEGATECALL, ...) type-asserts its
	// caller to *Contract (AsDelegate), so a bare AccountRef panics here —
	// build a real Contract frame instead, the way a nested DELEGATECALL
	// opcode would.
	delegateFrame := NewContract(AccountRef(caller), AccountRef(callee), uint256.NewInt(0), 100_000, false)
	if _, _, err := iv.DelegateCall(delegateFrame, callee, nil, 100_000); err != nil {
		t.Fatalf("DelegateCall failed: %v", err)
	}
	if _, _, err := iv.StaticCall(AccountRef(caller), callee, nil, 100_000); err != nil {
		t.Fatalf("StaticCall failed: %v", err)
	}
	if _, _, _, err := iv.Create(AccountRef(caller), []byte{byte(STOP)}, 100_000, uint256.NewInt(0)); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if _, _, _, err := iv.Create2(AccountRef(caller), []byte{byte(STOP)}, 100_000, uint256.NewInt(0), uint256.NewInt(1)); err != nil {
		t.Fatalf("Create2 failed: %v", err)
	}
	if _, _, _, err := iv.EOFCreate2(AccountRef(caller), []byte{byte(STOP)}, 100_000, uint256.NewInt(0), uint256.NewInt(2)); err == nil {
		t.Logf("EOFCreate2 succeeded without error (acceptable if EOF deploy is a no-op path here)")
	}

	stats := iv.Stats()
	if stats.CallCount != 0 || stats.CreateCount != 0 || stats.StaticCallCount != 0 || stats.DelegateCallCount != 0 {
		t.Fatalf("expected all counters to stay zero when disabled, got %+v", stats)
	}
}
