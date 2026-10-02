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

package logger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	modstate "github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func newLoggerScope(t *testing.T, contractAddr common.Address, stackVals []uint256.Int, memData []byte) *vm.ScopeContext {
	t.Helper()
	st := stack.New()
	for i := range stackVals {
		v := stackVals[i]
		st.Push(&v)
	}
	mem := vm.NewMemory()
	if len(memData) > 0 {
		mem.Resize(uint64(len(memData)))
		_ = mem.Set(0, uint64(len(memData)), memData)
	}
	contract := vm.NewContract(vm.AccountRef(contractAddr), vm.AccountRef(contractAddr), uint256.NewInt(0), 100000, true)
	return &vm.ScopeContext{Memory: mem, Stack: st, Contract: contract}
}

func newLoggerEVM(t *testing.T) *vm.EVM {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := modstate.New(modstate.NewPlainState(txDb, 1))
	blockCtx := evmtypes.BlockContext{BlockNumber: 1, Time: 0}
	return vm.NewEVM(blockCtx, evmtypes.TxContext{}, ibs, params.TestChainConfig, vm.Config{})
}

func TestStructLoggerCaptureFlow(t *testing.T) {
	l := NewStructLogger(&Config{EnableMemory: true, EnableReturnData: true})
	evm := newLoggerEVM(t)
	contractAddr := common.HexToAddress("0x01")

	l.CaptureStart(evm, common.HexToAddress("0xfrom"), contractAddr, false, nil, 1000, uint256.NewInt(0))

	scope := newLoggerScope(t, contractAddr, []uint256.Int{*uint256.NewInt(1)}, []byte("hello world padded to 32bytes!!"))
	l.CaptureState(0, vm.SLOAD, 100, 10, scope, []byte("ret"), 0, nil)

	if len(l.StructLogs()) != 1 {
		t.Fatalf("expected 1 struct log, got %d", len(l.StructLogs()))
	}
	entry := l.StructLogs()[0]
	if entry.Op != vm.SLOAD {
		t.Errorf("expected SLOAD op, got %v", entry.Op)
	}
	if entry.Storage == nil {
		t.Errorf("expected storage snapshot recorded for SLOAD")
	}

	l.CaptureFault(0, vm.SLOAD, 0, 0, scope, 0, errors.New("fault"))
	l.CaptureEnd([]byte{0x01}, 1000, nil)

	if l.Error() != nil {
		t.Errorf("expected no error, got %v", l.Error())
	}
	if len(l.Output()) != 1 {
		t.Errorf("expected output set")
	}

	res, err := l.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected non-empty result")
	}
}

func TestStructLoggerSstoreTracksStorage(t *testing.T) {
	l := NewStructLogger(nil)
	evm := newLoggerEVM(t)
	contractAddr := common.HexToAddress("0x02")
	l.CaptureStart(evm, common.HexToAddress("0xfrom"), contractAddr, false, nil, 1000, uint256.NewInt(0))

	scope := newLoggerScope(t, contractAddr, []uint256.Int{*uint256.NewInt(5), *uint256.NewInt(9)}, nil)
	l.CaptureState(0, vm.SSTORE, 100, 10, scope, nil, 0, nil)

	logs := l.StructLogs()
	if len(logs) != 1 || logs[0].Storage == nil {
		t.Fatalf("expected SSTORE to record a storage snapshot")
	}
}

func TestStructLoggerLimitStopsRecording(t *testing.T) {
	l := NewStructLogger(&Config{Limit: 1})
	evm := newLoggerEVM(t)
	addr := common.HexToAddress("0x03")
	l.CaptureStart(evm, common.HexToAddress("0xfrom"), addr, false, nil, 1000, uint256.NewInt(0))
	scope := newLoggerScope(t, addr, nil, nil)
	l.CaptureState(0, vm.ADD, 10, 1, scope, nil, 0, nil)
	l.CaptureState(1, vm.ADD, 10, 1, scope, nil, 0, nil)
	if len(l.StructLogs()) != 1 {
		t.Fatalf("expected limit to cap logs at 1, got %d", len(l.StructLogs()))
	}
}

func TestStructLoggerStopInterruptsCapture(t *testing.T) {
	l := NewStructLogger(nil)
	evm := newLoggerEVM(t)
	addr := common.HexToAddress("0x04")
	l.CaptureStart(evm, common.HexToAddress("0xfrom"), addr, false, nil, 1000, uint256.NewInt(0))
	l.Stop(errors.New("stopped"))
	scope := newLoggerScope(t, addr, nil, nil)
	l.CaptureState(0, vm.ADD, 10, 1, scope, nil, 0, nil)
	if len(l.StructLogs()) != 0 {
		t.Fatalf("expected no logs recorded after Stop")
	}
	if _, err := l.GetResult(); err == nil {
		t.Fatalf("expected GetResult to return the stop reason")
	}
}

func TestStructLoggerResetClearsState(t *testing.T) {
	l := NewStructLogger(nil)
	evm := newLoggerEVM(t)
	addr := common.HexToAddress("0x05")
	l.CaptureStart(evm, common.HexToAddress("0xfrom"), addr, false, nil, 1000, uint256.NewInt(0))
	scope := newLoggerScope(t, addr, nil, nil)
	l.CaptureState(0, vm.ADD, 10, 1, scope, nil, 0, nil)
	l.CaptureEnd(nil, 0, errors.New("boom"))

	l.Reset()
	if len(l.StructLogs()) != 0 || l.Error() != nil || l.Output() != nil {
		t.Fatalf("expected Reset to clear logs/err/output")
	}
}

func TestStructLoggerCaptureTxStartEnd(t *testing.T) {
	l := NewStructLogger(nil)
	l.CaptureTxStart(100000)
	l.CaptureTxEnd(60000)
	res, err := l.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected result with usedGas computed")
	}
}

func TestStructLoggerCaptureEnterExitNoOp(t *testing.T) {
	l := NewStructLogger(nil)
	// Should not panic; these are no-ops for StructLogger.
	l.CaptureEnter(vm.CALL, common.Address{}, common.Address{}, nil, 0, uint256.NewInt(0))
	l.CaptureExit(nil, 0, nil)
}

func TestWriteTraceAndWriteLogs(t *testing.T) {
	logs := []StructLog{
		{
			Pc:      1,
			Op:      vm.PUSH1,
			Gas:     100,
			GasCost: 3,
			Stack:   []uint256.Int{*uint256.NewInt(1), *uint256.NewInt(2)},
			Memory:  []byte("abcd"),
			Storage: map[common.Hash]common.Hash{common.HexToHash("0x01"): common.HexToHash("0x02")},
			Err:     errors.New("oops"),
		},
	}
	var buf bytes.Buffer
	WriteTrace(&buf, logs)
	if buf.Len() == 0 {
		t.Fatalf("expected non-empty trace output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("ERROR")) {
		t.Errorf("expected error annotation in trace output")
	}
}

func TestMarkdownLoggerFlow(t *testing.T) {
	var buf bytes.Buffer
	ml := NewMarkdownLogger(nil, &buf)
	evm := newLoggerEVM(t)
	addr := common.HexToAddress("0x06")

	ml.CaptureTxStart(21000)
	ml.CaptureStart(evm, common.HexToAddress("0xfrom"), addr, false, []byte{0x01}, 1000, uint256.NewInt(5))
	scope := newLoggerScope(t, addr, []uint256.Int{*uint256.NewInt(1)}, nil)
	ml.CaptureState(0, vm.ADD, 100, 3, scope, nil, 0, nil)
	ml.CaptureState(1, vm.ADD, 100, 3, scope, nil, 0, errors.New("step error"))
	ml.CaptureFault(2, vm.ADD, 0, 0, scope, 0, errors.New("fault"))
	ml.CaptureEnter(vm.CALL, addr, common.HexToAddress("0x07"), nil, 500, uint256.NewInt(0))
	ml.CaptureExit(nil, 100, nil)
	ml.CaptureEnd(nil, 900, nil)
	ml.CaptureTxEnd(0)

	out := buf.String()
	if out == "" {
		t.Fatalf("expected markdown output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Error:")) {
		t.Errorf("expected error line in markdown output")
	}
}

func TestNewMarkdownLoggerDefaultsConfig(t *testing.T) {
	var buf bytes.Buffer
	ml := NewMarkdownLogger(nil, &buf)
	if ml.cfg == nil {
		t.Fatalf("expected default config to be set")
	}
}
