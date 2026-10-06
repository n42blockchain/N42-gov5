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

package native

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
)

func TestFlatCallTracerRegistered(t *testing.T) {
	if _, err := tracers.DefaultDirectory.New("flatCallTracer", nil, nil); err != nil {
		t.Fatalf("flatCallTracer not registered: %v", err)
	}
}

func TestFlatCallTracerInvalidEmbeddedType(t *testing.T) {
	// Registering a tracer under the name "callTracer" with the wrong return
	// type isn't possible from the outside, so this exercises the ordinary
	// success path plus bad-json error path instead.
	if _, err := newFlatCallTracer(nil, json.RawMessage(`not-json`)); err == nil {
		t.Fatalf("expected error for invalid config json")
	}
}

func TestFlatCallTracerBasicCallFlow(t *testing.T) {
	ctx := &tracers.Context{BlockNumber: big.NewInt(42), TxIndex: 3}
	tr, err := newFlatCallTracer(ctx, nil)
	if err != nil {
		t.Fatalf("newFlatCallTracer: %v", err)
	}
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")

	ft.CaptureTxStart(100000)
	ft.CaptureStart(evm, from, to, false, []byte{0x01}, 90000, uint256.NewInt(0))
	ft.CaptureEnd([]byte{0x02}, 1000, nil)
	ft.CaptureTxEnd(89000)

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	if err := json.Unmarshal(res, &frames); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 flat frame, got %d", len(frames))
	}
	if frames[0]["type"] != "call" {
		t.Errorf("expected type call, got %v", frames[0]["type"])
	}
	if frames[0]["blockNumber"].(float64) != 42 {
		t.Errorf("expected blockNumber 42 from context, got %v", frames[0]["blockNumber"])
	}
}

func TestFlatCallTracerCreateFrame(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	ft.CaptureStart(evm, from, to, true, []byte{0xde, 0xad}, 90000, uint256.NewInt(5))
	ft.CaptureEnd([]byte{0xbe, 0xef}, 500, nil)

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	json.Unmarshal(res, &frames)
	if frames[0]["type"] != "create" {
		t.Errorf("expected create type, got %v", frames[0]["type"])
	}
}

func TestFlatCallTracerNestedCallsAndPrecompileFiltering(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	precompile := common.HexToAddress("0x9")
	ft.CaptureStart(evm, from, to, false, nil, 90000, uint256.NewInt(0))
	ft.activePrecompiles = []common.Address{precompile}

	// Nested call to a precompile should be dropped from the callstack by CaptureExit.
	ft.CaptureEnter(vm.CALL, to, precompile, nil, 1000, uint256.NewInt(0))
	ft.CaptureExit(nil, 100, nil)

	// Nested call to a regular contract should remain.
	regular := common.HexToAddress("0x03")
	ft.CaptureEnter(vm.CALL, to, regular, nil, 1000, uint256.NewInt(0))
	ft.CaptureExit(nil, 100, nil)

	ft.CaptureEnd(nil, 500, nil)

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	json.Unmarshal(res, &frames)
	// Top-level + 1 surviving nested call (precompile call removed).
	if len(frames) != 2 {
		t.Fatalf("expected 2 flat frames after precompile filtering, got %d: %s", len(frames), res)
	}
}

func TestFlatCallTracerIncludePrecompilesKeepsCall(t *testing.T) {
	cfg := json.RawMessage(`{"includePrecompiles":true}`)
	tr, err := newFlatCallTracer(nil, cfg)
	if err != nil {
		t.Fatalf("newFlatCallTracer: %v", err)
	}
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	precompile := common.HexToAddress("0x9")
	ft.CaptureStart(evm, from, to, false, nil, 90000, uint256.NewInt(0))
	ft.activePrecompiles = []common.Address{precompile}

	ft.CaptureEnter(vm.CALL, to, precompile, nil, 1000, uint256.NewInt(0))
	ft.CaptureExit(nil, 100, nil)
	ft.CaptureEnd(nil, 500, nil)

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	json.Unmarshal(res, &frames)
	if len(frames) != 2 {
		t.Fatalf("expected precompile call retained when includePrecompiles set, got %d frames", len(frames))
	}
}

func TestFlatCallTracerSelfDestruct(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()

	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	ft.CaptureStart(evm, from, to, false, nil, 90000, uint256.NewInt(0))

	refund := common.HexToAddress("0x05")
	ft.CaptureEnter(vm.SELFDESTRUCT, to, refund, nil, 0, nil)
	ft.CaptureExit(nil, 0, nil)
	ft.CaptureEnd(nil, 100, nil)

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	json.Unmarshal(res, &frames)
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames (call + suicide), got %d: %s", len(frames), res)
	}
	if frames[1]["type"] != "suicide" {
		t.Errorf("expected second frame type suicide, got %v", frames[1]["type"])
	}
}

func TestFlatCallTracerConvertParityErrors(t *testing.T) {
	cfg := json.RawMessage(`{"convertParityErrors":true}`)
	tr, err := newFlatCallTracer(nil, cfg)
	if err != nil {
		t.Fatalf("newFlatCallTracer: %v", err)
	}
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()
	ft.CaptureStart(evm, common.Address{}, common.Address{}, false, nil, 90000, uint256.NewInt(0))
	ft.CaptureEnd(nil, 0, errors.New("out of gas"))

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frames []map[string]any
	json.Unmarshal(res, &frames)
	if frames[0]["error"] != "Out of gas" {
		t.Errorf("expected parity-mapped error, got %v", frames[0]["error"])
	}
}

func TestFlatCallTracerUnrecognizedFrameType(t *testing.T) {
	// Directly exercise flatFromNested with an opcode that isn't CALL/CREATE/SELFDESTRUCT family.
	frame := &callFrame{Type: vm.ADD}
	if _, err := flatFromNested(frame, nil, false, nil); err == nil {
		t.Fatalf("expected error for unrecognized call frame type")
	}
}

func TestFlatCallTracerEmptyCallstackGetResult(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	ft.tracer.callstack = nil
	if _, err := ft.GetResult(); err == nil {
		t.Fatalf("expected error for empty callstack")
	}
}

// NOTE: flatCallTracer.Stop only forwards to the embedded callTracer's Stop
// (which sets callTracer.reason), but flatCallTracer.GetResult returns its
// own unrelated t.reason field, which Stop never sets. So the interruption
// reason is silently dropped at the flat-call layer. This looks like a
// latent defect in call_flat.go (not fixed here, per instructions); this
// test documents the current (surprising) behavior.
func TestFlatCallTracerStopDoesNotSurfaceReason(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	evm := newTestEVM()
	ft.CaptureStart(evm, common.Address{}, common.Address{}, false, nil, 0, uint256.NewInt(0))
	ft.CaptureEnd(nil, 0, nil)
	stopErr := errors.New("halt")
	ft.Stop(stopErr)
	_, err := ft.GetResult()
	if err != nil {
		t.Errorf("expected flatCallTracer.GetResult to not surface the stop reason (known defect), got %v", err)
	}
	if !errors.Is(ft.tracer.reason, stopErr) {
		t.Errorf("expected embedded callTracer to retain stop reason, got %v", ft.tracer.reason)
	}
}

func TestFlatCallTracerCaptureStateAndFaultDelegate(t *testing.T) {
	tr, _ := newFlatCallTracer(nil, nil)
	ft := tr.(*flatCallTracer)
	// Should not panic, simply delegate to embedded callTracer.
	ft.CaptureState(0, vm.ADD, 0, 0, nil, nil, 0, nil)
	ft.CaptureFault(0, vm.ADD, 0, 0, nil, 0, nil)
}
