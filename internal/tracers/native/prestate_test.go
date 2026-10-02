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
	"strings"
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

func newPrestateEVM(t *testing.T) *vm.EVM {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := modstate.New(modstate.NewPlainState(txDb, 1))

	from := common.HexToAddress("0xf000")
	to := common.HexToAddress("0xf001")
	ibs.CreateAccount(from, true)
	ibs.AddBalance(from, uint256.NewInt(1_000_000))
	ibs.SetNonce(from, 1)
	ibs.CreateAccount(to, true)
	ibs.AddBalance(to, uint256.NewInt(500))

	blockCtx := evmtypes.BlockContext{BlockNumber: 1, Time: 0, Coinbase: common.HexToAddress("0xc0ffee")}
	txCtx := evmtypes.TxContext{GasPrice: uint256.NewInt(1)}
	return vm.NewEVM(blockCtx, txCtx, ibs, params.TestChainConfig, vm.Config{})
}

func TestPrestateTracerRegistered(t *testing.T) {
	for _, name := range []string{"prestateTracer"} {
		if _, err := tracers.DefaultDirectory.New(name, nil, nil); err != nil {
			t.Fatalf("%s not registered: %v", name, err)
		}
	}
}

func TestPrestateTracerBasicCaptureNonDiff(t *testing.T) {
	tr, err := newPrestateTracer(nil, nil)
	if err != nil {
		t.Fatalf("newPrestateTracer: %v", err)
	}
	pt := tr.(*prestateTracer)
	evm := newPrestateEVM(t)

	from := common.HexToAddress("0xf000")
	to := common.HexToAddress("0xf001")

	pt.CaptureTxStart(21000)
	pt.CaptureStart(evm, from, to, false, nil, 21000, uint256.NewInt(100))
	pt.CaptureEnd(nil, 21000, nil)

	res, err := pt.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var pre map[string]map[string]any
	if err := json.Unmarshal(res, &pre); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := pre[strings.ToLower(from.Hex())]; !ok {
		t.Errorf("expected from account in prestate, got %s", res)
	}
	if _, ok := pre[strings.ToLower(to.Hex())]; !ok {
		t.Errorf("expected to account in prestate, got %s", res)
	}
}

func TestPrestateTracerCreateExcludesNewContract(t *testing.T) {
	tr, _ := newPrestateTracer(nil, nil)
	pt := tr.(*prestateTracer)
	evm := newPrestateEVM(t)

	from := common.HexToAddress("0xf000")
	newContract := common.HexToAddress("0xdead01")

	pt.CaptureTxStart(21000)
	pt.CaptureStart(evm, from, newContract, true, nil, 21000, uint256.NewInt(0))
	pt.CaptureEnd(nil, 21000, nil)

	res, err := pt.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var pre map[string]map[string]any
	json.Unmarshal(res, &pre)
	if _, ok := pre[strings.ToLower(newContract.Hex())]; ok {
		t.Errorf("expected newly created contract excluded from prestate, got %s", res)
	}
}

func TestPrestateTracerDiffModeCapturesPost(t *testing.T) {
	cfg := json.RawMessage(`{"diffMode":true}`)
	tr, err := newPrestateTracer(nil, cfg)
	if err != nil {
		t.Fatalf("newPrestateTracer: %v", err)
	}
	pt := tr.(*prestateTracer)
	evm := newPrestateEVM(t)

	from := common.HexToAddress("0xf000")
	to := common.HexToAddress("0xf001")

	pt.CaptureTxStart(21000)
	pt.CaptureStart(evm, from, to, false, nil, 21000, uint256.NewInt(100))
	// Mutate state after the "transaction" to simulate an effect.
	evm.IntraBlockState().SetNonce(to, 5)
	pt.CaptureTxEnd(0)

	res, err := pt.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	var obj struct {
		Post map[string]map[string]any `json:"post"`
		Pre  map[string]map[string]any `json:"pre"`
	}
	if err := json.Unmarshal(res, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := obj.Post[strings.ToLower(to.Hex())]; !ok {
		t.Errorf("expected modified account %s in post state, got %s", to.Hex(), res)
	}
}

func TestPrestateTracerCaptureStateSloadTracksStorage(t *testing.T) {
	tr, _ := newPrestateTracer(nil, nil)
	pt := tr.(*prestateTracer)
	evm := newPrestateEVM(t)
	contractAddr := common.HexToAddress("0xf001")

	pt.CaptureTxStart(21000)
	pt.CaptureStart(evm, common.HexToAddress("0xf000"), contractAddr, false, nil, 21000, uint256.NewInt(0))

	slot := uint256.NewInt(7)
	scope := newScope(contractAddr, []uint256.Int{*slot}, nil)
	pt.CaptureState(0, vm.SLOAD, 100, 0, scope, nil, 1, nil)

	if _, ok := pt.pre[contractAddr].Storage[common.Hash(slot.Bytes32())]; !ok {
		t.Errorf("expected SLOAD to record storage slot")
	}
}

func TestPrestateTracerStopSetsReason(t *testing.T) {
	tr, _ := newPrestateTracer(nil, nil)
	pt := tr.(*prestateTracer)
	stopErr := errors.New("stopped")
	pt.Stop(stopErr)
	_, err := pt.GetResult()
	if !errors.Is(err, stopErr) {
		t.Errorf("expected stop error propagated, got %v", err)
	}
}
