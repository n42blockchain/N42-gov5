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
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// vmTRecordingTracer implements EVMLogger and records every hook call for
// assertions, guarded by a mutex even though the interpreter is single
// threaded per call (cheap insurance, and avoids -race false positives if a
// future test ever shares one across goroutines).
type vmTRecordingTracer struct {
	mu          sync.Mutex
	txStarts    int
	txEnds      int
	starts      int
	ends        int
	enters      int
	exits       int
	states      int
	faults      int
	lastErr     error
	lastFaultOp OpCode
}

func (r *vmTRecordingTracer) CaptureTxStart(gasLimit uint64) { r.mu.Lock(); r.txStarts++; r.mu.Unlock() }
func (r *vmTRecordingTracer) CaptureTxEnd(restGas uint64)    { r.mu.Lock(); r.txEnds++; r.mu.Unlock() }
func (r *vmTRecordingTracer) CaptureStart(env VMInterface, from, to types.Address, create bool, input []byte, gas uint64, value *uint256.Int) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
}
func (r *vmTRecordingTracer) CaptureEnd(output []byte, usedGas uint64, err error) {
	r.mu.Lock()
	r.ends++
	r.mu.Unlock()
}
func (r *vmTRecordingTracer) CaptureEnter(typ OpCode, from, to types.Address, input []byte, gas uint64, value *uint256.Int) {
	r.mu.Lock()
	r.enters++
	r.mu.Unlock()
}
func (r *vmTRecordingTracer) CaptureExit(output []byte, usedGas uint64, err error) {
	r.mu.Lock()
	r.exits++
	r.mu.Unlock()
}
func (r *vmTRecordingTracer) CaptureState(pc uint64, op OpCode, gas, cost uint64, scope *ScopeContext, rData []byte, depth int, err error) {
	r.mu.Lock()
	r.states++
	r.mu.Unlock()
}
func (r *vmTRecordingTracer) CaptureFault(pc uint64, op OpCode, gas, cost uint64, scope *ScopeContext, depth int, err error) {
	r.mu.Lock()
	r.faults++
	r.lastErr = err
	r.lastFaultOp = op
	r.mu.Unlock()
}

// TestVMTTracerHooksTopLevelCall drives a full top-level EVM.Call with a
// Debug tracer and asserts CaptureStart/CaptureState/CaptureEnd all fired.
func TestVMTTracerHooksTopLevelCall(t *testing.T) {
	tracer := &vmTRecordingTracer{}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	evm.config.Debug = true
	evm.config.Tracer = tracer
	// Re-point the interpreter at the updated config (interpreter is built
	// once at NewEVM time and captures evm.config by value for cfg).
	evm.interpreter = NewEVMInterpreter(evm, evm.config)

	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if tracer.starts == 0 {
		t.Errorf("expected CaptureStart to fire")
	}
	if tracer.ends == 0 {
		t.Errorf("expected CaptureEnd to fire")
	}
	if tracer.states == 0 {
		t.Errorf("expected CaptureState to fire per opcode")
	}
}

// TestVMTTracerCaptureFaultOnError drives execution into a runtime error
// that occurs inside an opcode's own execute function (an invalid JUMP
// destination), which happens *after* that step's CaptureState has already
// logged — exactly the condition that routes to CaptureFault instead of a
// second CaptureState (see EVMInterpreter.Run's deferred tracer dispatch:
// the stack/gas pre-checks error out before logging and hit CaptureState,
// but an in-execute error after logging hits CaptureFault).
func TestVMTTracerCaptureFaultOnError(t *testing.T) {
	tracer := &vmTRecordingTracer{}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	evm.config.Debug = true
	evm.config.Tracer = tracer
	evm.interpreter = NewEVMInterpreter(evm, evm.config)

	code := []byte{
		byte(PUSH1), 0x05, // jump target 5, not a JUMPDEST
		byte(JUMP),
		byte(0), byte(0),
		byte(STOP), // pc5: not a JUMPDEST
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err == nil {
		t.Fatalf("expected invalid jump destination error")
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if tracer.faults == 0 {
		t.Errorf("expected CaptureFault to fire on invalid jump destination")
	}
}

// TestVMTStaticCallReadOnlyViolations verifies that every state-changing
// opcode is rejected with ErrWriteProtection when reached via STATICCALL.
func TestVMTStaticCallReadOnlyViolations(t *testing.T) {
	cases := []struct {
		name string
		code []byte
	}{
		{"SSTORE", []byte{byte(PUSH1), 0x01, byte(PUSH1), 0x00, byte(SSTORE)}},
		{"LOG0", []byte{byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(LOG0)}},
		{"CREATE", []byte{byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(CREATE)}},
		{"CREATE2", []byte{byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(PUSH1), 0x00, byte(CREATE2)}},
		{"SELFDESTRUCT", []byte{byte(PUSH1), 0x00, byte(SELFDESTRUCT)}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
			callee := types.HexToAddress("0x00000000000000000000000000000000feed01")
			ibs.CreateAccount(callee, true)
			ibs.SetCode(callee, c.code)

			caller := types.HexToAddress("0x00000000000000000000000000000000ca11e4")
			ibs.CreateAccount(caller, true)
			ibs.PrepareAccessList(caller, &callee, nil, nil)

			_, _, err := evm.StaticCall(AccountRef(caller), callee, nil, 1_000_000)
			if err != ErrWriteProtection {
				t.Fatalf("%s under STATICCALL: got err=%v, want ErrWriteProtection", c.name, err)
			}
		})
	}
}

// TestVMTCallDepthLimit verifies CALL fails with ErrDepth once the
// interpreter depth exceeds params.CallCreateDepth, without needing to
// actually recurse 1024 times.
func TestVMTCallDepthLimit(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	callee := types.HexToAddress("0x00000000000000000000000000000000feed02")
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{byte(STOP)})
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11e5")
	ibs.CreateAccount(caller, true)

	interp := evm.interpreter.(*EVMInterpreter)
	interp.depth = int(params.CallCreateDepth) + 1

	_, _, err := evm.Call(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(0), false)
	if err != ErrDepth {
		t.Fatalf("expected ErrDepth, got %v", err)
	}
}

// TestVMTCallInsufficientBalance verifies CALL with a nonzero value fails
// with ErrInsufficientBalance when the caller cannot cover it.
func TestVMTCallInsufficientBalance(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	callee := types.HexToAddress("0x00000000000000000000000000000000feed03")
	ibs.CreateAccount(callee, true)
	ibs.SetCode(callee, []byte{byte(STOP)})
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11e6")
	ibs.CreateAccount(caller, true) // zero balance

	_, _, err := evm.Call(AccountRef(caller), callee, nil, 100_000, uint256.NewInt(1), false)
	if err != ErrInsufficientBalance {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

// TestVMTCreateCodeSizeLimitExceeded verifies deployment fails with
// ErrMaxCodeSizeExceeded when the returned init-code output is larger than
// params.MaxCodeSize.
func TestVMTCreateCodeSizeLimitExceeded(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11e7")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	// Init code that returns a runtime blob 1 byte larger than MaxCodeSize.
	// RETURN(offset=0, size=MaxCodeSize+1). The memory is zero-filled, so no
	// explicit content is needed, just the requested size.
	size := uint32(params.MaxCodeSize + 1)
	initCode := []byte{
		byte(PUSH3), byte(size >> 16), byte(size >> 8), byte(size), // size
		byte(PUSH1), 0x00, // offset
		byte(RETURN),
	}

	_, _, _, err := evm.create(AccountRef(caller), &codeAndHash{code: initCode}, 10_000_000, uint256.NewInt(0), types.Address{}, CREATE, false, false)
	if err != ErrMaxCodeSizeExceeded {
		t.Fatalf("expected ErrMaxCodeSizeExceeded, got %v", err)
	}
}
