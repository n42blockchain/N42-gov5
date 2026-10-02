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
	"errors"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

func TestNoopTracerAllHooksAreNoOps(t *testing.T) {
	tr, err := newNoopTracer(nil, nil)
	if err != nil {
		t.Fatalf("newNoopTracer error: %v", err)
	}
	nt, ok := tr.(*noopTracer)
	if !ok {
		t.Fatalf("expected *noopTracer")
	}

	// None of these should panic.
	nt.CaptureStart(nil, common.Address{}, common.Address{}, false, nil, 0, uint256.NewInt(0))
	nt.CaptureState(0, vm.ADD, 0, 0, nil, nil, 0, nil)
	nt.CaptureFault(0, vm.ADD, 0, 0, nil, 0, nil)
	nt.CaptureEnter(vm.CALL, common.Address{}, common.Address{}, nil, 0, uint256.NewInt(0))
	nt.CaptureExit(nil, 0, nil)
	nt.CaptureTxStart(21000)
	nt.CaptureTxEnd(0)
	nt.CaptureEnd(nil, 0, nil)
	nt.Stop(errors.New("x"))

	res, err := nt.GetResult()
	if err != nil {
		t.Fatalf("GetResult error: %v", err)
	}
	if string(res) != "{}" {
		t.Errorf("expected empty object result, got %s", res)
	}
}

func TestNoopTracerRegistered(t *testing.T) {
	if _, err := tracers.DefaultDirectory.New("noopTracer", nil, nil); err != nil {
		t.Fatalf("noopTracer not registered: %v", err)
	}
}

func newTestEVM() *vm.EVM {
	blockCtx := evmtypes.BlockContext{BlockNumber: 1, Time: 0}
	return vm.NewEVM(blockCtx, evmtypes.TxContext{}, nil, params.TestChainConfig, vm.Config{})
}

func TestFourByteTracerRegistered(t *testing.T) {
	if _, err := tracers.DefaultDirectory.New("4byteTracer", nil, nil); err != nil {
		t.Fatalf("4byteTracer not registered: %v", err)
	}
}

func TestFourByteTracerCapturesOuterCalldata(t *testing.T) {
	tr, err := newFourByteTracer(nil, nil)
	if err != nil {
		t.Fatalf("newFourByteTracer error: %v", err)
	}
	ft := tr.(*fourByteTracer)
	evm := newTestEVM()

	input := append([]byte{0x12, 0x34, 0x56, 0x78}, make([]byte, 32)...)
	ft.CaptureStart(evm, common.Address{}, common.Address{}, false, input, 50000, uint256.NewInt(0))

	res, err := ft.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if string(res) == "{}" {
		t.Fatalf("expected non-empty selector map, got %s", res)
	}
}

func TestFourByteTracerCaptureEnterFiltersOpcodes(t *testing.T) {
	tr, _ := newFourByteTracer(nil, nil)
	ft := tr.(*fourByteTracer)

	input := append([]byte{0xaa, 0xbb, 0xcc, 0xdd}, make([]byte, 10)...)

	// CREATE should be ignored.
	ft.CaptureEnter(vm.CREATE, common.Address{}, common.Address{}, input, 1000, uint256.NewInt(0))
	if len(ft.ids) != 0 {
		t.Fatalf("expected CREATE to be ignored, got %v", ft.ids)
	}

	// CALL with short input should be ignored.
	ft.CaptureEnter(vm.CALL, common.Address{}, common.Address{}, []byte{0x01}, 1000, uint256.NewInt(0))
	if len(ft.ids) != 0 {
		t.Fatalf("expected short input to be ignored, got %v", ft.ids)
	}

	// CALL with valid input should be tracked.
	ft.CaptureEnter(vm.CALL, common.Address{}, common.Address{0x9}, input, 1000, uint256.NewInt(0))
	if len(ft.ids) != 1 {
		t.Fatalf("expected 1 tracked selector, got %v", ft.ids)
	}

	// Precompile should be skipped.
	ft.activePrecompiles = []common.Address{common.Address{0x9}}
	before := len(ft.ids)
	ft.CaptureEnter(vm.STATICCALL, common.Address{}, common.Address{0x9}, input, 1000, uint256.NewInt(0))
	if len(ft.ids) != before {
		t.Fatalf("expected precompile call to be skipped")
	}
}

func TestFourByteTracerStopPropagatesReason(t *testing.T) {
	tr, _ := newFourByteTracer(nil, nil)
	ft := tr.(*fourByteTracer)
	stopErr := errors.New("halted")
	ft.Stop(stopErr)
	_, err := ft.GetResult()
	if !errors.Is(err, stopErr) {
		t.Errorf("expected stop reason, got %v", err)
	}
}

func TestFourByteTracerInterruptBlocksCaptureEnter(t *testing.T) {
	tr, _ := newFourByteTracer(nil, nil)
	ft := tr.(*fourByteTracer)
	ft.Stop(errors.New("stop"))
	input := append([]byte{0x01, 0x02, 0x03, 0x04}, make([]byte, 4)...)
	ft.CaptureEnter(vm.CALL, common.Address{}, common.Address{}, input, 1000, uint256.NewInt(0))
	if len(ft.ids) != 0 {
		t.Errorf("expected no tracking after interrupt, got %v", ft.ids)
	}
}
