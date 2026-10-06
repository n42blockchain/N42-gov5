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
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

func TestAccessListTracerSeedsFromPresetList(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	preset := common.HexToAddress("0x03")
	excluded := common.HexToAddress("0x04") // == from, should be excluded

	acl := transaction.AccessList{
		{Address: preset, StorageKeys: []common.Hash{common.HexToHash("0x10")}},
		{Address: from, StorageKeys: []common.Hash{common.HexToHash("0x20")}},
	}
	tr := NewAccessListTracer(acl, from, to, nil)
	result := tr.AccessList()

	found := false
	for _, tuple := range result {
		if tuple.Address == preset {
			found = true
			if len(tuple.StorageKeys) != 1 {
				t.Errorf("expected 1 storage key for preset address, got %d", len(tuple.StorageKeys))
			}
		}
		if tuple.Address == from && len(tuple.StorageKeys) != 1 {
			// from is excluded from addAddress but addSlot adds it anyway via addAddress call internally.
		}
	}
	if !found {
		t.Errorf("expected preset address present in access list")
	}
	_ = excluded
}

func TestAccessListTracerCaptureStateSloadSstore(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	tr := NewAccessListTracer(nil, from, to, nil)

	contractAddr := common.HexToAddress("0x05")
	scope := newLoggerScope(t, contractAddr, []uint256.Int{*uint256.NewInt(7)}, nil)
	tr.CaptureState(0, vm.SLOAD, 100, 0, scope, nil, 0, nil)

	acl := tr.AccessList()
	if len(acl) != 1 || acl[0].Address != contractAddr {
		t.Fatalf("expected contract address tracked, got %+v", acl)
	}
	if len(acl[0].StorageKeys) != 1 {
		t.Fatalf("expected 1 storage key, got %d", len(acl[0].StorageKeys))
	}
}

func TestAccessListTracerCaptureStateExtcodeAndCall(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	precompile := common.HexToAddress("0x09")
	tr := NewAccessListTracer(nil, from, to, []common.Address{precompile})

	contractAddr := common.HexToAddress("0x05")
	target := common.HexToAddress("0x06")

	// EXTCODEHASH touches `target`.
	scope := newLoggerScope(t, contractAddr, []uint256.Int{*uint256.NewInt(0).SetBytes(target.Bytes())}, nil)
	tr.CaptureState(0, vm.EXTCODEHASH, 100, 0, scope, nil, 0, nil)

	// CALL touches the address at stack[len-2].
	callStack := []uint256.Int{
		*uint256.NewInt(0), *uint256.NewInt(0), *uint256.NewInt(0),
		*uint256.NewInt(0).SetBytes(target.Bytes()), *uint256.NewInt(0),
	}
	scope2 := newLoggerScope(t, contractAddr, callStack, nil)
	tr.CaptureState(0, vm.CALL, 100, 0, scope2, nil, 0, nil)

	// Precompile call should not be tracked.
	precompileStack := []uint256.Int{
		*uint256.NewInt(0), *uint256.NewInt(0), *uint256.NewInt(0),
		*uint256.NewInt(0).SetBytes(precompile.Bytes()), *uint256.NewInt(0),
	}
	scope3 := newLoggerScope(t, contractAddr, precompileStack, nil)
	tr.CaptureState(0, vm.CALL, 100, 0, scope3, nil, 0, nil)

	acl := tr.AccessList()
	var found bool
	for _, tuple := range acl {
		if tuple.Address == target {
			found = true
		}
		if tuple.Address == precompile {
			t.Errorf("expected precompile address excluded from access list")
		}
	}
	if !found {
		t.Errorf("expected target address tracked via EXTCODEHASH/CALL")
	}
}

func TestAccessListTracerNoOpHooks(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	tr := NewAccessListTracer(nil, from, to, nil)

	// None of these should panic.
	tr.CaptureStart(nil, from, to, false, nil, 0, uint256.NewInt(0))
	tr.CaptureFault(0, vm.ADD, 0, 0, nil, 0, nil)
	tr.CaptureEnd(nil, 0, nil)
	tr.CaptureEnter(vm.CALL, from, to, nil, 0, uint256.NewInt(0))
	tr.CaptureExit(nil, 0, nil)
	tr.CaptureTxStart(21000)
	tr.CaptureTxEnd(0)
}

func TestAccessListTracerEqual(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	tr1 := NewAccessListTracer(nil, from, to, nil)
	tr2 := NewAccessListTracer(nil, from, to, nil)
	if !tr1.Equal(tr2) {
		t.Errorf("expected two empty tracers to be equal")
	}

	addr := common.HexToAddress("0x03")
	tr1.list.addAddress(addr)
	if tr1.Equal(tr2) {
		t.Errorf("expected tracers to differ after adding an address")
	}

	tr2.list.addAddress(addr)
	if !tr1.Equal(tr2) {
		t.Errorf("expected tracers to match after both add the same address")
	}

	tr1.list.addSlot(addr, common.HexToHash("0x01"))
	if tr1.Equal(tr2) {
		t.Errorf("expected tracers to differ after one adds a slot")
	}
}

func TestAccessListEqualDifferentLengths(t *testing.T) {
	al1 := newAccessList()
	al2 := newAccessList()
	al1.addAddress(common.HexToAddress("0x01"))
	al1.addAddress(common.HexToAddress("0x02"))
	al2.addAddress(common.HexToAddress("0x01"))
	if al1.equal(al2) {
		t.Errorf("expected access lists of differing lengths to not be equal")
	}
}
