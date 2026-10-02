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

package js

import (
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	modstate "github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// TestStepWrappersExerciseLogAccessors drives a step function that touches
// the stack/memory/contract/db wrapper objects exposed to JS tracer scripts,
// covering the Get*/peek/slice accessor methods that a no-op tracer never
// reaches.
func TestStepWrappersExerciseLogAccessors(t *testing.T) {
	code := `{
		seen: {},
		step: function(log, db) {
			this.seen.pc = log.getPC();
			this.seen.gas = log.getGas();
			this.seen.cost = log.getCost();
			this.seen.depth = log.getDepth();
			this.seen.refund = log.getRefund();
			this.seen.err = log.getError();
			this.seen.opNum = log.op.toNumber();
			this.seen.opStr = log.op.toString();
			this.seen.isPush = log.op.isPush();
			this.seen.stackLen = log.stack.length();
			if (this.seen.stackLen > 0) {
				this.seen.top = log.stack.peek(0).toString();
			}
			this.seen.memLen = log.memory.length();
			if (this.seen.memLen > 0) {
				log.memory.slice(0, 1);
				log.memory.getUint(0);
			}
			this.seen.caller = toHex(log.contract.getCaller());
			this.seen.address = toHex(log.contract.getAddress());
			this.seen.value = log.contract.getValue().toString();
			this.seen.input = toHex(log.contract.getInput());
			var addr = log.contract.getAddress();
			this.seen.balance = db.getBalance(addr).toString();
		},
		fault: function() {},
		result: function() { return this.seen; }
	}`
	tracer, err := newJsTracer(code, new(tracers.Context), nil)
	if err != nil {
		t.Fatalf("newJsTracer: %v", err)
	}
	// PUSH1 1 PUSH1 2 ADD STOP: gives the step function non-empty stack and
	// a PUSH opcode to inspect on the very first step.
	contractCode := []byte{byte(vm.PUSH1), 0x01, byte(vm.PUSH1), 0x02, byte(vm.ADD), byte(vm.STOP)}
	res, err := runTrace(tracer, testCtx(), params.TestChainConfig, contractCode)
	if err != nil {
		t.Fatalf("runTrace: %v", err)
	}
	var seen map[string]any
	if err := json.Unmarshal(res, &seen); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	for _, key := range []string{"pc", "opNum", "opStr", "isPush", "stackLen", "memLen", "caller", "address", "value", "input", "balance"} {
		if _, ok := seen[key]; !ok {
			t.Errorf("expected key %q present in step result, got %+v", key, seen)
		}
	}
}

// TestDBWrapperAccessorsAgainstRealState exercises the dbObj
// GetNonce/GetCode/GetState/Exists wrappers against a real (non-stub)
// IntraBlockState backed by an in-memory database, since the shared
// dummyStatedb test helper only stubs GetRefund/GetBalance.
func TestDBWrapperAccessorsAgainstRealState(t *testing.T) {
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := modstate.New(modstate.NewPlainState(txDb, 1))

	addr := common.HexToAddress("0xcafe")
	ibs.CreateAccount(addr, true)
	ibs.SetNonce(addr, 42)
	ibs.SetCode(addr, []byte{0x60, 0x60})

	code := `{
		info: {},
		step: function(log, db) {
			var addr = log.contract.getAddress();
			this.info.nonce = db.getNonce(addr);
			this.info.code = toHex(db.getCode(addr));
			this.info.state = toHex(db.getState(addr, toWord(new Uint8Array(32))));
			this.info.exists = db.exists(addr);
			this.info.existsMissing = db.exists(toAddress('000000000000000000000000000000deadbeef'));
		},
		fault: function() {},
		result: function() { return this.info; }
	}`
	tracer, err := newJsTracer(code, new(tracers.Context), nil)
	if err != nil {
		t.Fatalf("newJsTracer: %v", err)
	}

	env := vm.NewEVM(evmtypes.BlockContext{BlockNumber: 1}, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, ibs, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})
	contract := vm.NewContract(vm.AccountRef(addr), vm.AccountRef(addr), uint256.NewInt(0), 100000, false)
	contract.Code = []byte{byte(vm.STOP)}

	tracer.CaptureTxStart(100000)
	tracer.CaptureStart(env, contract.Caller(), contract.Address(), false, nil, 100000, uint256.NewInt(0))
	if _, err := env.Interpreter().Run(contract, nil, false); err != nil {
		t.Fatalf("interpreter run: %v", err)
	}
	tracer.CaptureEnd(nil, 0, nil)
	tracer.CaptureTxEnd(contract.Gas)

	res, err := tracer.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal(res, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["nonce"].(float64) != 42 {
		t.Errorf("expected nonce 42, got %v", info["nonce"])
	}
	if info["code"] != "0x6060" {
		t.Errorf("expected code 0x6060, got %v", info["code"])
	}
	if info["exists"] != true {
		t.Errorf("expected exists true for known account")
	}
	if info["existsMissing"] != false {
		t.Errorf("expected exists false for unknown account")
	}
}

// TestEnterExitFrameAccessors exercises the callframe / callframeResult
// wrapper getters (getType/getFrom/getTo/getInput/getValue/getOutput/getError)
// beyond what TestEnterExit in tracer_test.go already checks.
func TestEnterExitFrameAccessors(t *testing.T) {
	code := `{
		info: {},
		step: function() {},
		fault: function() {},
		enter: function(frame) {
			this.info.type = frame.getType();
			this.info.from = toHex(frame.getFrom());
			this.info.to = toHex(frame.getTo());
			this.info.input = toHex(frame.getInput());
			this.info.gas = frame.getGas();
			this.info.value = frame.getValue() === undefined ? null : frame.getValue().toString();
		},
		exit: function(res) {
			this.info.gasUsed = res.getGasUsed();
			this.info.output = toHex(res.getOutput());
			this.info.err = res.getError();
		},
		result: function() { return this.info; }
	}`
	tracer, err := newJsTracer(code, new(tracers.Context), nil)
	if err != nil {
		t.Fatalf("newJsTracer: %v", err)
	}
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	tracer.CaptureEnter(vm.CALL, from, to, []byte{0xde, 0xad}, 50000, uint256.NewInt(7))
	tracer.CaptureExit([]byte{0xbe, 0xef}, 1000, nil)

	res, err := tracer.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal(res, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["type"] != "CALL" {
		t.Errorf("expected type CALL, got %v", info["type"])
	}
	if info["output"] != "0xbeef" {
		t.Errorf("expected output 0xbeef, got %v", info["output"])
	}
}

// TestEnterExitFrameNilValue exercises callframe.GetValue's nil branch
// (STATICCALL carries no value).
func TestEnterExitFrameNilValue(t *testing.T) {
	code := `{
		gotUndefined: false,
		step: function() {},
		fault: function() {},
		enter: function(frame) { this.gotUndefined = (frame.getValue() === undefined); },
		exit: function() {},
		result: function() { return this.gotUndefined; }
	}`
	tracer, err := newJsTracer(code, new(tracers.Context), nil)
	if err != nil {
		t.Fatalf("newJsTracer: %v", err)
	}
	tracer.CaptureEnter(vm.STATICCALL, common.Address{}, common.Address{}, nil, 1000, nil)
	tracer.CaptureExit(nil, 0, nil)

	res, err := tracer.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if string(res) != "true" {
		t.Errorf("expected getValue() to be undefined for nil value, got %s", res)
	}
}
