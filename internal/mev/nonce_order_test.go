package mev

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TestOptimizeOrdering_PreservesNonceOrder covers preserveNonceOrder: with
// optimizationLevel > 0 and two transactions from the same sender submitted
// with nonces out of order, the optimizer must re-sort that sender's
// transactions into ascending nonce order even though the generic score
// sort would otherwise place them in whatever order their tips dictate.
func TestOptimizeOrdering_PreservesNonceOrder(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 1, false, nil)

	sender := types.HexToAddress("0x1111111111111111111111111111111111111111")
	other := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// Same sender: nonce 1 carries a higher tip than nonce 0, so the raw
	// score sort would rank nonce 1 ahead of nonce 0 absent reordering.
	txNonce1 := makeDynFeeTx(1, sender, nil, uint256.NewInt(100), uint256.NewInt(200), 21000, uint256.NewInt(0))
	txNonce0 := makeDynFeeTx(0, sender, nil, uint256.NewInt(1), uint256.NewInt(200), 21000, uint256.NewInt(0))
	txOther := makeDynFeeTx(0, other, nil, uint256.NewInt(50), uint256.NewInt(200), 21000, uint256.NewInt(0))

	baseFee := uint256.NewInt(10)
	result := opt.OptimizeOrdering([]*transaction.Transaction{txNonce1, txNonce0, txOther}, baseFee)

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}

	var pos0, pos1 = -1, -1
	for i, tx := range result {
		if tx.From() == nil {
			continue
		}
		if *tx.From() == sender {
			if tx.Nonce() == 0 {
				pos0 = i
			} else if tx.Nonce() == 1 {
				pos1 = i
			}
		}
	}
	if pos0 == -1 || pos1 == -1 {
		t.Fatalf("did not find both sender transactions in result: pos0=%d pos1=%d", pos0, pos1)
	}
	if pos0 >= pos1 {
		t.Fatalf("nonce order not preserved: nonce 0 at %d, nonce 1 at %d, want nonce 0 before nonce 1", pos0, pos1)
	}
}

// TestOptimizeOrdering_NonceOrderSkippedAtLevelZero covers the branch where
// optimizationLevel == 0, so preserveNonceOrder is never invoked and the
// result is ordered purely by score.
func TestOptimizeOrdering_NonceOrderSkippedAtLevelZero(t *testing.T) {
	opt := NewAIBlockOptimizer(false, 0, false, nil)

	sender := types.HexToAddress("0x3333333333333333333333333333333333333333")
	txNonce1 := makeDynFeeTx(1, sender, nil, uint256.NewInt(100), uint256.NewInt(200), 21000, uint256.NewInt(0))
	txNonce0 := makeDynFeeTx(0, sender, nil, uint256.NewInt(1), uint256.NewInt(200), 21000, uint256.NewInt(0))

	baseFee := uint256.NewInt(10)
	result := opt.OptimizeOrdering([]*transaction.Transaction{txNonce1, txNonce0}, baseFee)
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}
