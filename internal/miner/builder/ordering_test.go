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

package builder

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestTxByPriceAndNonceOrdering(t *testing.T) {
	baseFee := uint256.NewInt(1e9) // 1 Gwei

	// Create 3 accounts with different tip levels.
	addr1 := types.HexToAddress("0x01")
	addr2 := types.HexToAddress("0x02")
	addr3 := types.HexToAddress("0x03")

	pending := map[types.Address][]*transaction.Transaction{
		addr1: {makeTx(0, 5), makeTx(1, 5)},   // 5 Gwei tip
		addr2: {makeTx(0, 20), makeTx(1, 20)},  // 20 Gwei tip
		addr3: {makeTx(0, 10), makeTx(1, 10)},  // 10 Gwei tip
	}

	txSet := NewTxByPriceAndNonce(pending, baseFee)

	// Drain all transactions and collect tips.
	var tips []uint64
	for txSet.Peek() != nil {
		tx := txSet.Peek()
		tips = append(tips, tx.GasTipCap().Uint64())
		txSet.Shift()
	}

	// 3 accounts × 2 txs = 6.
	if len(tips) != 6 {
		t.Fatalf("expected 6 txs, got %d", len(tips))
	}

	// First tx should have highest tip (20 Gwei).
	if tips[0] != 20e9 {
		t.Fatalf("expected first tip 20 Gwei, got %d", tips[0])
	}

	// Each account's txs should be consecutive (same tip), overall descending.
	// Expected order: 20, 20, 10, 10, 5, 5 (Gwei, as raw uint64)
	for i := 1; i < len(tips); i++ {
		if tips[i] > tips[i-1] {
			t.Fatalf("tips not in descending order at index %d: %d > %d", i, tips[i], tips[i-1])
		}
	}
}

func TestTxByPriceAndNonceEmpty(t *testing.T) {
	txSet := NewTxByPriceAndNonce(nil, nil)
	if txSet.Peek() != nil {
		t.Fatal("empty set should return nil")
	}
}
