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

func makeTx(nonce uint64, tipGwei uint64) *transaction.Transaction {
	tip := new(uint256.Int).Mul(uint256.NewInt(tipGwei), uint256.NewInt(1e9))
	feeCap := new(uint256.Int).Add(tip, uint256.NewInt(1e9))
	inner := &transaction.DynamicFeeTx{
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       21000,
	}
	return transaction.NewTx(inner)
}

func TestBundlePoolBasic(t *testing.T) {
	pool := NewBundlePool()

	tx1 := makeTx(0, 10)
	tx2 := makeTx(1, 20)

	bundle := &Bundle{
		Txs:         []*transaction.Transaction{tx1, tx2},
		BlockNumber: 100,
	}

	if err := pool.AddBundle(bundle); err != nil {
		t.Fatal(err)
	}

	bundles := pool.GetBundles(100, 0)
	if len(bundles) != 1 {
		t.Fatalf("expected 1 bundle, got %d", len(bundles))
	}

	// Different block number should return nothing.
	bundles = pool.GetBundles(101, 0)
	if len(bundles) != 0 {
		t.Fatalf("expected 0 bundles for block 101, got %d", len(bundles))
	}
}

func TestBundlePoolPrune(t *testing.T) {
	pool := NewBundlePool()

	for i := uint64(10); i < 15; i++ {
		pool.AddBundle(&Bundle{
			Txs:         []*transaction.Transaction{makeTx(0, 5)},
			BlockNumber: i,
		})
	}

	pool.PruneBundles(13)

	// Blocks 10, 11, 12 should be pruned.
	for i := uint64(10); i < 13; i++ {
		if bundles := pool.GetBundles(i, 0); len(bundles) != 0 {
			t.Fatalf("block %d should be pruned", i)
		}
	}
	// Block 13, 14 should remain.
	if bundles := pool.GetBundles(13, 0); len(bundles) != 1 {
		t.Fatal("block 13 should still have a bundle")
	}
}

func TestBundlePoolPriceOrdering(t *testing.T) {
	pool := NewBundlePool()

	// Add bundles with different prices.
	for _, tip := range []uint64{5, 20, 10, 15} {
		pool.AddBundle(&Bundle{
			Txs:         []*transaction.Transaction{makeTx(0, tip)},
			BlockNumber: 100,
		})
	}

	bundles := pool.GetBundles(100, 0)
	if len(bundles) != 4 {
		t.Fatalf("expected 4 bundles, got %d", len(bundles))
	}

	// Should be sorted by price descending.
	for i := 1; i < len(bundles); i++ {
		if bundles[i].BundlePrice.Cmp(bundles[i-1].BundlePrice) > 0 {
			t.Fatalf("bundles not sorted by price: %s > %s at index %d",
				bundles[i].BundlePrice, bundles[i-1].BundlePrice, i)
		}
	}
}

func TestBundlePoolTimestampFilter(t *testing.T) {
	pool := NewBundlePool()

	pool.AddBundle(&Bundle{
		Txs:          []*transaction.Transaction{makeTx(0, 5)},
		BlockNumber:  100,
		MinTimestamp:  1000,
		MaxTimestamp:  2000,
	})

	// Too early.
	if bundles := pool.GetBundles(100, 500); len(bundles) != 0 {
		t.Fatal("should be filtered by min timestamp")
	}

	// In range.
	if bundles := pool.GetBundles(100, 1500); len(bundles) != 1 {
		t.Fatal("should be included in range")
	}

	// Too late.
	if bundles := pool.GetBundles(100, 2500); len(bundles) != 0 {
		t.Fatal("should be filtered by max timestamp")
	}
}

func TestBundleRevertAllowed(t *testing.T) {
	tx := makeTx(0, 10)
	hash := tx.Hash()
	other := types.HexToHash("0x1234")

	bundle := &Bundle{
		Txs:               []*transaction.Transaction{tx},
		BlockNumber:        100,
		RevertingTxHashes:  []types.Hash{hash},
	}

	if !bundle.IsRevertAllowed(hash) {
		t.Fatal("should allow revert for listed hash")
	}
	if bundle.IsRevertAllowed(other) {
		t.Fatal("should not allow revert for unlisted hash")
	}
}

func TestBundleValidation(t *testing.T) {
	pool := NewBundlePool()

	// Empty bundle.
	if err := pool.AddBundle(&Bundle{BlockNumber: 100}); err != ErrBundleEmpty {
		t.Fatalf("expected ErrBundleEmpty, got %v", err)
	}

	// No block number.
	if err := pool.AddBundle(&Bundle{Txs: []*transaction.Transaction{makeTx(0, 1)}}); err != ErrBundleBlockRange {
		t.Fatalf("expected ErrBundleBlockRange, got %v", err)
	}
}
