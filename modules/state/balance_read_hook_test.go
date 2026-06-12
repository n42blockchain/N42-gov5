// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// TestBalanceReadHook asserts the lazy-coinbase observer contract:
//   - GetBalance (the BALANCE/SELFBALANCE/CanTransfer entry) FIRES the hook;
//   - AddBalance on an untouched account is a blind increment (balanceInc)
//     and must NOT fire it — the deferred tip credit stays invisible;
//   - other account reads (GetNonce) don't fire it either (their values are
//     unaffected by a deferred balance credit).
func TestBalanceReadHook(t *testing.T) {
	reader := newMVMockReader()
	ibs := New(reader)

	coinbase := mkAddr(0xcb)
	other := mkAddr(0x01)

	var observed []types.Address
	ibs.SetBalanceReadHook(func(a types.Address) { observed = append(observed, a) })

	// Blind tip credit: no read, no hook.
	ibs.AddBalance(coinbase, uint256.NewInt(7))
	if len(observed) != 0 {
		t.Fatalf("blind AddBalance fired the hook: %v", observed)
	}

	// Non-balance read: no hook.
	_ = ibs.GetNonce(coinbase)
	if len(observed) != 0 {
		t.Fatalf("GetNonce fired the hook: %v", observed)
	}

	// Explicit balance reads fire, with the right address.
	_ = ibs.GetBalance(other)
	_ = ibs.GetBalance(coinbase)
	if len(observed) != 2 || observed[0] != other || observed[1] != coinbase {
		t.Fatalf("observed = %v, want [other, coinbase]", observed)
	}

	// Hook clears.
	ibs.SetBalanceReadHook(nil)
	_ = ibs.GetBalance(coinbase)
	if len(observed) != 2 {
		t.Fatal("cleared hook still firing")
	}
}
