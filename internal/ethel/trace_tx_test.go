// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// ethTScope builds a minimal ScopeContext with a stack holding the given
// values, enough to exercise CaptureState/CaptureFault's stack-formatting
// paths in both NoopTracer and StepTracer.
func ethTScope(values ...uint64) *vm2.ScopeContext {
	st := stack.New()
	for _, v := range values {
		st.Push(uint256.NewInt(v))
	}
	return &vm2.ScopeContext{Stack: st}
}

func TestNoopTracerAllMethodsAreNoops(t *testing.T) {
	var tr NoopTracer
	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	val := uint256.NewInt(1)

	tr.CaptureTxStart(100)
	tr.CaptureTxEnd(50)
	tr.CaptureStart(nil, from, to, false, nil, 100, val)
	tr.CaptureEnd(nil, 50, nil)
	tr.CaptureEnter(vm2.CALL, from, to, nil, 100, val)
	tr.CaptureExit(nil, 50, nil)
	tr.CaptureFault(0, vm2.ADD, 10, 3, ethTScope(1, 2), 1, errors.New("boom"))

	// CaptureState exercises the noopLevel-gated branches. noopLevel is a
	// package-level var fixed at init from an env var not set during tests,
	// so it is 0 here — just confirm it doesn't panic and does nothing.
	tr.CaptureState(0, vm2.ADD, 10, 3, ethTScope(1, 2), nil, 1, nil)
	tr.CaptureState(0, vm2.ADD, 10, 3, nil, nil, 1, nil)
}

func TestStepTracerProducesValidStructLogJSON(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStepTracer(&buf)

	from := types.HexToAddress("0xaa")
	to := types.HexToAddress("0xbb")
	val := uint256.NewInt(7)

	tr.CaptureTxStart(1000)
	tr.CaptureStart(nil, from, to, false, []byte{1, 2, 3}, 1000, val)
	tr.CaptureEnter(vm2.CALL, from, to, nil, 500, val)
	tr.CaptureExit(nil, 400, nil)

	// First CaptureState: non-empty stack, no error.
	tr.CaptureState(0, vm2.PUSH1, 1000, 3, ethTScope(0, 42), nil, 1, nil)
	// Second CaptureState: stack with a zero top value (exercises the
	// "0x0" branch) plus an error (exercises errStr branch).
	tr.CaptureState(1, vm2.ADD, 997, 3, ethTScope(0), nil, 1, errors.New("stack underflow"))
	// CaptureFault delegates to CaptureState.
	tr.CaptureFault(2, vm2.STOP, 994, 0, ethTScope(), 1, nil)

	tr.CaptureEnd([]byte{0xde, 0xad}, 994, nil)
	tr.CaptureTxEnd(994)
	tr.Close(false, 994, []byte{0xde, 0xad})

	out := buf.String()
	if !strings.HasPrefix(out, `{"structLogs":[`) {
		t.Fatalf("missing structLogs prefix: %s", out)
	}
	if !strings.Contains(out, `"error":"stack underflow"`) {
		t.Fatalf("missing error field: %s", out)
	}
	if !strings.Contains(out, `"0x0"`) {
		t.Fatalf("missing zero-stack-value marker: %s", out)
	}
	if !strings.Contains(out, `"0x2a"`) {
		t.Fatalf("missing nonzero stack value 0x2a: %s", out)
	}
	if !strings.Contains(out, `"failed":false,"gas":994,"returnValue":"dead"`) {
		t.Fatalf("missing Close trailer: %s", out)
	}
	if !strings.Contains(out, `/*captureStart: from=`) {
		t.Fatalf("missing captureStart comment: %s", out)
	}
}

func TestStepTracerCloseFailedTrue(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStepTracer(&buf)
	tr.Close(true, 0, nil)
	if !strings.Contains(buf.String(), `"failed":true,"gas":0,"returnValue":""`) {
		t.Fatalf("unexpected close output: %s", buf.String())
	}
}
