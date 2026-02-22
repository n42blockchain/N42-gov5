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
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type account struct{}

func (account) SubBalance(amount *big.Int)                          {}
func (account) AddBalance(amount *big.Int)                          {}
func (account) SetAddress(common.Address)                           {}
func (account) Value() *big.Int                                     { return nil }
func (account) SetBalance(*big.Int)                                 {}
func (account) SetNonce(uint64)                                     {}
func (account) Balance() *big.Int                                   { return nil }
func (account) Address() common.Address                             { return common.Address{} }
func (account) SetCode(common.Hash, []byte)                         {}
func (account) ForEachStorage(cb func(key, value common.Hash) bool) {}

type dummyStatedb struct {
	state.IntraBlockState
}

func (*dummyStatedb) GetRefund() uint64                           { return 1337 }
func (*dummyStatedb) GetBalance(addr common.Address) *uint256.Int { return new(uint256.Int) }

type vmContext struct {
	blockCtx evmtypes.BlockContext
	txCtx    evmtypes.TxContext
}

func testCtx() *vmContext {
	return &vmContext{blockCtx: evmtypes.BlockContext{BlockNumber: 1}, txCtx: evmtypes.TxContext{GasPrice: uint256.NewInt(100000)}}
}

func runTrace(tracer tracers.Tracer, vmctx *vmContext, chaincfg *params.ChainConfig, contractCode []byte) (json.RawMessage, error) {
	var (
		env             = vm.NewEVM(vmctx.blockCtx, vmctx.txCtx, &dummyStatedb{}, chaincfg, vm.Config{Debug: true, Tracer: tracer})
		gasLimit uint64 = 31000
		startGas uint64 = 10000
		value           = uint256.NewInt(0)
		contract        = vm.NewContract(account{}, account{}, value, startGas, false)
	)
	contract.Code = []byte{byte(vm.PUSH1), 0x1, byte(vm.PUSH1), 0x1, 0x0}
	if contractCode != nil {
		contract.Code = contractCode
	}

	tracer.CaptureTxStart(gasLimit)
	tracer.CaptureStart(env, contract.Caller(), contract.Address(), false, []byte{}, startGas, value)
	ret, err := env.Interpreter().Run(contract, []byte{}, false)
	tracer.CaptureEnd(ret, startGas-contract.Gas, err)
	// Rest gas assumes no refund
	tracer.CaptureTxEnd(contract.Gas)
	if err != nil {
		return nil, err
	}
	return tracer.GetResult()
}

func TestHalt(t *testing.T) {
	timeout := errors.New("stahp")
	tracer, err := newJsTracer("{step: function() { while(1); }, result: function() { return null; }, fault: function(){}}", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1 * time.Second)
		tracer.Stop(timeout)
	}()
	if _, err = runTrace(tracer, testCtx(), params.TestChainConfig, nil); !strings.Contains(err.Error(), "stahp") {
		t.Errorf("Expected timeout error, got %v", err)
	}
}

func TestHaltBetweenSteps(t *testing.T) {
	tracer, err := newJsTracer("{step: function() {}, fault: function() {}, result: function() { return null; }}", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := vm.NewEVM(evmtypes.BlockContext{BlockNumber: 1}, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})
	scope := &vm.ScopeContext{
		Contract: vm.NewContract(&account{}, &account{}, uint256.NewInt(0), 0, false),
	}
	tracer.CaptureStart(env, common.Address{}, common.Address{}, false, []byte{}, 0, uint256.NewInt(0))
	tracer.CaptureState(0, 0, 0, 0, scope, nil, 0, nil)
	timeout := errors.New("stahp")
	tracer.Stop(timeout)
	tracer.CaptureState(0, 0, 0, 0, scope, nil, 0, nil)

	if _, err := tracer.GetResult(); !strings.Contains(err.Error(), timeout.Error()) {
		t.Errorf("Expected timeout error, got %v", err)
	}
}

// testNoStepExec tests a regular value transfer (no exec), and accessing the statedb
// in 'result'
func TestNoStepExec(t *testing.T) {
	execTracer := func(code string) []byte {
		t.Helper()
		tracer, err := newJsTracer(code, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		env := vm.NewEVM(evmtypes.BlockContext{BlockNumber: 1}, evmtypes.TxContext{GasPrice: uint256.NewInt(100)}, &dummyStatedb{}, params.TestChainConfig, vm.Config{Debug: true, Tracer: tracer})
		tracer.CaptureStart(env, common.Address{}, common.Address{}, false, []byte{}, 1000, uint256.NewInt(0))
		tracer.CaptureEnd(nil, 0, nil)
		ret, err := tracer.GetResult()
		if err != nil {
			t.Fatal(err)
		}
		return ret
	}
	for i, tt := range []struct {
		code string
		want string
	}{
		{ // tests that we don't panic on accessing the db methods
			code: "{depths: [], step: function() {}, fault: function() {},  result: function(ctx, db){ return db.getBalance(ctx.to)} }",
			want: `"0"`,
		},
	} {
		if have := execTracer(tt.code); tt.want != string(have) {
			t.Errorf("testcase %d: expected return value to be %s got %s\n\tcode: %v", i, tt.want, string(have), tt.code)
		}
	}
}

func TestIsPrecompile(t *testing.T) {
	chaincfg := &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(100),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(200),
		MuirGlacierBlock:      big.NewInt(0),
		BerlinBlock:           big.NewInt(300),
		LondonBlock:           big.NewInt(0),
		Ethash:                new(params.EthashConfig),
	}
	txCtx := evmtypes.TxContext{GasPrice: uint256.NewInt(100000)}
	tracer, err := newJsTracer("{addr: toAddress('0000000000000000000000000000000000000009'), res: null, step: function() { this.res = isPrecompiled(this.addr); }, fault: function() {}, result: function() { return this.res; }}", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	blockCtx := evmtypes.BlockContext{BlockNumber: 150}
	res, err := runTrace(tracer, &vmContext{blockCtx, txCtx}, chaincfg, nil)
	if err != nil {
		t.Error(err)
	}
	if string(res) != "false" {
		t.Errorf("tracer should not consider blake2f as precompile in byzantium")
	}

	tracer, _ = newJsTracer("{addr: toAddress('0000000000000000000000000000000000000009'), res: null, step: function() { this.res = isPrecompiled(this.addr); }, fault: function() {}, result: function() { return this.res; }}", nil, nil)
	blockCtx = evmtypes.BlockContext{BlockNumber: 250}
	res, err = runTrace(tracer, &vmContext{blockCtx, txCtx}, chaincfg, nil)
	if err != nil {
		t.Error(err)
	}
	if string(res) != "true" {
		t.Errorf("tracer should consider blake2f as precompile in istanbul")
	}
}

func TestEnterExit(t *testing.T) {
	// test that either both or none of enter() and exit() are defined
	if _, err := newJsTracer("{step: function() {}, fault: function() {}, result: function() { return null; }, enter: function() {}}", new(tracers.Context), nil); err == nil {
		t.Fatal("tracer creation should've failed without exit() definition")
	}
	if _, err := newJsTracer("{step: function() {}, fault: function() {}, result: function() { return null; }, enter: function() {}, exit: function() {}}", new(tracers.Context), nil); err != nil {
		t.Fatal(err)
	}
	// test that the enter and exit method are correctly invoked and the values passed
	tracer, err := newJsTracer("{enters: 0, exits: 0, enterGas: 0, gasUsed: 0, step: function() {}, fault: function() {}, result: function() { return {enters: this.enters, exits: this.exits, enterGas: this.enterGas, gasUsed: this.gasUsed} }, enter: function(frame) { this.enters++; this.enterGas = frame.getGas(); }, exit: function(res) { this.exits++; this.gasUsed = res.getGasUsed(); }}", new(tracers.Context), nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := &vm.ScopeContext{
		Contract: vm.NewContract(&account{}, &account{}, uint256.NewInt(0), 0, false),
	}
	tracer.CaptureEnter(vm.CALL, scope.Contract.Caller(), scope.Contract.Address(), []byte{}, 1000, new(uint256.Int))
	tracer.CaptureExit([]byte{}, 400, nil)

	have, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"enters":1,"exits":1,"enterGas":1000,"gasUsed":400}`
	if string(have) != want {
		t.Errorf("Number of invocations of enter() and exit() is wrong. Have %s, want %s\n", have, want)
	}
}

func TestSetup(t *testing.T) {
	// Test empty config
	_, err := newJsTracer(`{setup: function(cfg) { if (cfg !== "{}") { throw("invalid empty config") } }, fault: function() {}, result: function() {}}`, new(tracers.Context), nil)
	if err != nil {
		t.Error(err)
	}

	cfg, err := json.Marshal(map[string]string{"foo": "bar"})
	if err != nil {
		t.Fatal(err)
	}
	// Test no setup func
	_, err = newJsTracer(`{fault: function() {}, result: function() {}}`, new(tracers.Context), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Test config value
	tracer, err := newJsTracer("{config: null, setup: function(cfg) { this.config = JSON.parse(cfg) }, step: function() {}, fault: function() {}, result: function() { return this.config.foo }}", new(tracers.Context), cfg)
	if err != nil {
		t.Fatal(err)
	}
	have, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != `"bar"` {
		t.Errorf("tracer returned wrong result. have: %s, want: \"bar\"\n", string(have))
	}
}
