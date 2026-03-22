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

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestPredictedExecutor_NoPredictor(t *testing.T) {
	numTxs := 8
	txs := make([]*transaction.Transaction, numTxs)
	for i := range txs {
		addr := types.Address{byte(i + 1)}
		txs[i] = makeTx(addrPtr(addr), []byte{0x01, 0x02, 0x03, 0x04})
	}

	pe := NewPredictedExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex)})
		return nil
	}, nil, txs)

	results := pe.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	// With nil predictor, orderMap should be identity.
	for i, orig := range pe.orderMap {
		if orig != i {
			t.Errorf("expected identity orderMap[%d]=%d, got %d", i, i, orig)
		}
	}
}

func TestPredictedExecutor_WithPredictor(t *testing.T) {
	// Create txs where some target the same contract with same selector
	// to trigger conflict grouping by the predictor.
	addr := types.Address{0xAA}
	otherAddr := types.Address{0xBB}
	sel := []byte{0x11, 0x22, 0x33, 0x44}

	txs := []*transaction.Transaction{
		makeTx(addrPtr(addr), sel),      // 0: conflict (same contract+selector as 2, 4)
		makeTx(addrPtr(otherAddr), sel), // 1: independent (only tx to otherAddr)
		makeTx(addrPtr(addr), sel),      // 2: conflict
		makeTx(nil, []byte{0x60, 0x00}), // 3: contract creation (independent)
		makeTx(addrPtr(addr), sel),      // 4: conflict
	}
	numTxs := len(txs)

	dp := NewDependencyPredictor(64)

	// Track which original indices were executed.
	executed := make([]bool, numTxs)

	pe := NewPredictedExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		executed[txIndex] = true
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex * 10)})
		return nil
	}, dp, txs)

	results := pe.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}

	// All txs should have been executed.
	for i, e := range executed {
		if !e {
			t.Errorf("tx %d was not executed", i)
		}
	}

	// Results should be in original order.
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	// Verify that reordering happened: conflicting txs (0, 2, 4) should
	// appear before non-conflicting (1) and independent (3) in orderMap.
	conflictGroup := make(map[int]bool)
	for _, idx := range []int{0, 2, 4} {
		conflictGroup[idx] = true
	}
	// The first 3 positions in orderMap should contain the conflict group.
	for i := 0; i < 3; i++ {
		if !conflictGroup[pe.orderMap[i]] {
			t.Errorf("expected conflict group member at orderMap[%d], got original index %d", i, pe.orderMap[i])
		}
	}
}

func TestPredictedExecutor_SmallBatch(t *testing.T) {
	// With <= 4 txs, predictor should not reorder (identity map).
	numTxs := 4
	txs := make([]*transaction.Transaction, numTxs)
	addr := types.Address{0xAA}
	sel := []byte{0x11, 0x22, 0x33, 0x44}
	for i := range txs {
		txs[i] = makeTx(addrPtr(addr), sel) // all same contract+selector
	}

	dp := NewDependencyPredictor(64)

	pe := NewPredictedExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex)})
		return nil
	}, dp, txs)

	results := pe.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}

	// Small batch: identity mapping even though predictor is non-nil.
	for i, orig := range pe.orderMap {
		if orig != i {
			t.Errorf("expected identity for small batch: orderMap[%d]=%d, got %d", i, i, orig)
		}
	}
}
