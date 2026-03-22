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

package parallel

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// makeTx creates a simple legacy transaction targeting 'to' with the given calldata.
func makeTx(to *types.Address, data []byte) *transaction.Transaction {
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		Gas:      21000,
		GasPrice: uint256.NewInt(1),
		Value:    uint256.NewInt(0),
		To:       to,
		Data:     data,
	})
}

func addrPtr(a types.Address) *types.Address { return &a }

func TestPredictor_AllIndependent(t *testing.T) {
	dp := NewDependencyPredictor(64)
	// N txs to N different contracts -> identity order (no conflicts).
	txs := make([]*transaction.Transaction, 5)
	for i := range txs {
		addr := types.Address{byte(i + 1)}
		txs[i] = makeTx(addrPtr(addr), []byte{0x01, 0x02, 0x03, 0x04})
	}

	order := dp.PredictOrder(txs)
	if len(order) != 5 {
		t.Fatalf("expected 5 indices, got %d", len(order))
	}
	// All are non-conflicting single-tx groups; verify all indices present.
	seen := make(map[int]bool)
	for _, idx := range order {
		seen[idx] = true
	}
	for i := 0; i < 5; i++ {
		if !seen[i] {
			t.Errorf("missing index %d", i)
		}
	}
}

func TestPredictor_SameContract(t *testing.T) {
	dp := NewDependencyPredictor(64)
	addr := types.Address{0xAA}

	// Two txs to the same contract with same selector -> conflict group.
	sel := []byte{0xAB, 0xCD, 0xEF, 0x01}
	txs := []*transaction.Transaction{
		makeTx(addrPtr(addr), sel),
		makeTx(addrPtr(addr), sel),
	}

	order := dp.PredictOrder(txs)
	if len(order) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(order))
	}
	// Both should be in the conflict group (first in output).
	if order[0] != 0 || order[1] != 1 {
		t.Errorf("expected [0,1], got %v", order)
	}
}

func TestPredictor_SameSelector(t *testing.T) {
	dp := NewDependencyPredictor(64)
	addr := types.Address{0xBB}
	sel := []byte{0x11, 0x22, 0x33, 0x44}

	// 3 txs with same selector to same contract.
	txs := []*transaction.Transaction{
		makeTx(addrPtr(addr), sel),
		makeTx(addrPtr(addr), sel),
		makeTx(addrPtr(addr), sel),
	}

	order := dp.PredictOrder(txs)
	if len(order) != 3 {
		t.Fatalf("expected 3 indices, got %d", len(order))
	}
	// All in conflict group, original order preserved.
	for i, idx := range order {
		if idx != i {
			t.Errorf("expected order[%d]=%d, got %d", i, i, idx)
		}
	}
}

func TestPredictor_HistoricalOverlap(t *testing.T) {
	dp := NewDependencyPredictor(64)
	addr := types.Address{0xCC}

	selA := FuncSelector{0x01, 0x02, 0x03, 0x04}
	selB := FuncSelector{0x05, 0x06, 0x07, 0x08}

	sharedSlot := types.Hash{0xFF}

	// Record overlapping slot access.
	dp.RecordAccess(addr, selA, []types.Hash{sharedSlot, {0x01}})
	dp.RecordAccess(addr, selB, []types.Hash{sharedSlot, {0x02}})

	txs := []*transaction.Transaction{
		makeTx(addrPtr(addr), selA[:]),
		makeTx(addrPtr(addr), selB[:]),
	}

	order := dp.PredictOrder(txs)
	if len(order) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(order))
	}
	// Should be grouped as conflicting (first in output).
	if order[0] != 0 || order[1] != 1 {
		t.Errorf("expected conflict group [0,1], got %v", order)
	}
}

func TestPredictor_PreservesOrder(t *testing.T) {
	dp := NewDependencyPredictor(64)
	addrA := types.Address{0xDD}
	addrB := types.Address{0xEE}
	sel := []byte{0x11, 0x22, 0x33, 0x44}

	// Mix of conflict and independent txs.
	txs := []*transaction.Transaction{
		makeTx(addrPtr(addrA), sel), // 0: conflict group (same contract+selector as 2)
		makeTx(addrPtr(addrB), sel), // 1: independent (only tx to addrB)
		makeTx(addrPtr(addrA), sel), // 2: conflict group
	}

	order := dp.PredictOrder(txs)
	if len(order) != 3 {
		t.Fatalf("expected 3 indices, got %d", len(order))
	}
	// Conflict group [0, 2] first, then non-conflicting [1].
	if order[0] != 0 {
		t.Errorf("expected first conflict index 0, got %d", order[0])
	}
	if order[1] != 2 {
		t.Errorf("expected second conflict index 2, got %d", order[1])
	}
	if order[2] != 1 {
		t.Errorf("expected independent index 1, got %d", order[2])
	}
}

func TestPredictor_NilTo(t *testing.T) {
	dp := NewDependencyPredictor(64)

	// Contract creation txs (nil To) are always independent.
	txs := []*transaction.Transaction{
		makeTx(nil, []byte{0x60, 0x00}),
		makeTx(nil, []byte{0x60, 0x01}),
	}

	order := dp.PredictOrder(txs)
	if len(order) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(order))
	}
	// Both in independent group.
	if order[0] != 0 || order[1] != 1 {
		t.Errorf("expected [0,1], got %v", order)
	}
}

func TestPredictor_Decay(t *testing.T) {
	dp := NewDependencyPredictor(64)
	addr := types.Address{0xFF}
	selA := FuncSelector{0x01, 0x02, 0x03, 0x04}

	dp.RecordAccess(addr, selA, []types.Hash{{0x01}, {0x02}})

	// Verify history exists.
	dp.mu.RLock()
	if len(dp.history) == 0 {
		t.Fatal("expected history before decay")
	}
	dp.mu.RUnlock()

	dp.Decay()

	// Verify history cleared.
	dp.mu.RLock()
	if len(dp.history) != 0 {
		t.Errorf("expected empty history after decay, got %d entries", len(dp.history))
	}
	dp.mu.RUnlock()
}
