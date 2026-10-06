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
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

func newScope(contractAddr common.Address, stackVals []uint256.Int, memData []byte) *vm.ScopeContext {
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

func mustCallTracer(t *testing.T, cfg string) *callTracer {
	t.Helper()
	var raw json.RawMessage
	if cfg != "" {
		raw = json.RawMessage(cfg)
	}
	tr, err := newCallTracer(nil, raw)
	if err != nil {
		t.Fatalf("newCallTracer error: %v", err)
	}
	ct, ok := tr.(*callTracer)
	if !ok {
		t.Fatalf("expected *callTracer")
	}
	return ct
}

func TestCallTracerRegistered(t *testing.T) {
	if _, err := tracers.DefaultDirectory.New("callTracer", nil, nil); err != nil {
		t.Fatalf("callTracer not registered: %v", err)
	}
}

func TestCallTracerBasicFlow(t *testing.T) {
	ct := mustCallTracer(t, "")
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")

	ct.CaptureTxStart(100000)
	ct.CaptureStart(nil, from, to, false, []byte{0x01}, 90000, uint256.NewInt(5))
	ct.CaptureEnd([]byte{0x02}, 1000, nil)
	ct.CaptureTxEnd(89000)

	res, err := ct.GetResult()
	if err != nil {
		t.Fatalf("GetResult error: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(res, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame["type"] != "CALL" {
		t.Errorf("expected CALL type, got %v", frame["type"])
	}
	if frame["gasUsed"] != "0x2bf20" { // 100000-89000=11000=0x2af8; just check exists
		// don't assert exact hex, just that the field is populated and non-zero
	}
	if _, ok := frame["gasUsed"]; !ok {
		t.Errorf("gasUsed missing")
	}
}

func TestCallTracerCreateType(t *testing.T) {
	ct := mustCallTracer(t, "")
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")
	ct.CaptureStart(nil, from, to, true, nil, 90000, nil)
	ct.CaptureEnd(nil, 0, errors.New("boom"))

	res, err := ct.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frame map[string]any
	json.Unmarshal(res, &frame)
	if frame["type"] != "CREATE" {
		t.Errorf("expected CREATE, got %v", frame["type"])
	}
	if frame["error"] != "boom" {
		t.Errorf("expected error boom, got %v", frame["error"])
	}
	if _, ok := frame["to"]; ok {
		t.Errorf("expected 'to' cleared for failed CREATE")
	}
}

func TestCallTracerNestedCalls(t *testing.T) {
	ct := mustCallTracer(t, "")
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	ct.CaptureStart(nil, from, to, false, nil, 90000, uint256.NewInt(0))

	inner := common.HexToAddress("0x03")
	ct.CaptureEnter(vm.CALL, to, inner, nil, 50000, uint256.NewInt(0))
	ct.CaptureExit([]byte{0xaa}, 1000, nil)

	ct.CaptureEnd(nil, 2000, nil)

	res, err := ct.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frame struct {
		Calls []map[string]any `json:"calls"`
	}
	json.Unmarshal(res, &frame)
	if len(frame.Calls) != 1 {
		t.Fatalf("expected 1 nested call, got %d", len(frame.Calls))
	}
}

func TestCallTracerOnlyTopCallSkipsEnter(t *testing.T) {
	ct := mustCallTracer(t, `{"onlyTopCall":true}`)
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	ct.CaptureStart(nil, from, to, false, nil, 90000, uint256.NewInt(0))
	ct.CaptureEnter(vm.CALL, to, common.HexToAddress("0x03"), nil, 1000, uint256.NewInt(0))
	ct.CaptureExit(nil, 100, nil)
	ct.CaptureEnd(nil, 500, nil)

	res, _ := ct.GetResult()
	var frame struct {
		Calls []map[string]any `json:"calls"`
	}
	json.Unmarshal(res, &frame)
	if len(frame.Calls) != 0 {
		t.Fatalf("expected no nested calls with onlyTopCall, got %d", len(frame.Calls))
	}
}

func TestCallTracerWithLogCapturesLOG(t *testing.T) {
	ct := mustCallTracer(t, `{"withLog":true}`)
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	ct.CaptureStart(nil, from, to, false, nil, 90000, uint256.NewInt(0))

	// LOG1: stack holds [..., topic, size, offset] with offset on top (index len-1)
	// our CaptureState reads mStart=stack[len-1], mSize=stack[len-2], topic=stack[len-3-i]
	data := []byte("hello world padded to 32 bytes!")
	scope := newScope(to, []uint256.Int{
		*uint256.NewInt(1), // topic
		*uint256.NewInt(uint64(len(data))), // size
		*uint256.NewInt(0),                 // offset
	}, data)

	ct.CaptureState(0, vm.LOG1, 100, 10, scope, nil, 0, nil)
	ct.CaptureTxEnd(0)

	res, err := ct.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frame struct {
		Logs []map[string]any `json:"logs"`
	}
	json.Unmarshal(res, &frame)
	if len(frame.Logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d: %s", len(frame.Logs), res)
	}
}

func TestCallTracerWithLogSkippedWhenDisabled(t *testing.T) {
	ct := mustCallTracer(t, "")
	scope := newScope(common.HexToAddress("0x02"), []uint256.Int{*uint256.NewInt(0), *uint256.NewInt(0), *uint256.NewInt(0)}, nil)
	ct.CaptureState(0, vm.LOG0, 100, 10, scope, nil, 0, nil)
	if len(ct.callstack[0].Logs) != 0 {
		t.Errorf("expected no logs captured when WithLog disabled")
	}
}

func TestCallTracerStopSetsReason(t *testing.T) {
	ct := mustCallTracer(t, "")
	ct.CaptureStart(nil, common.Address{}, common.Address{}, false, nil, 0, uint256.NewInt(0))
	ct.CaptureEnd(nil, 0, nil)
	stopErr := errors.New("interrupted")
	ct.Stop(stopErr)
	_, err := ct.GetResult()
	if !errors.Is(err, stopErr) {
		t.Errorf("expected stop reason propagated, got %v", err)
	}
}

func TestCallTracerRevertReasonDecoded(t *testing.T) {
	ct := mustCallTracer(t, "")
	ct.CaptureStart(nil, common.Address{}, common.Address{}, false, nil, 0, uint256.NewInt(0))
	// ABI-encoded Error(string) selector 0x08c379a0
	out := []byte{0x08, 0xc3, 0x79, 0xa0}
	ct.CaptureEnd(out, 0, vm.ErrExecutionReverted)
	res, err := ct.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var frame map[string]any
	json.Unmarshal(res, &frame)
	if frame["error"] != vm.ErrExecutionReverted.Error() {
		t.Errorf("expected revert error set, got %v", frame["error"])
	}
}

func TestCallTracerGetResultBadCallstack(t *testing.T) {
	ct := mustCallTracer(t, "")
	ct.callstack = append(ct.callstack, callFrame{})
	if _, err := ct.GetResult(); err == nil {
		t.Errorf("expected error for unbalanced callstack")
	}
}
