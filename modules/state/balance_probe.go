// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// Balance probe kinds (BalanceProbe).
const (
	BalanceProbeNone    = 0 // neither an object nor a pending increase
	BalanceProbePending = 1 // a pending increase, the account not read
	BalanceProbeObject  = 2 // a live object: its balance
)

// BalanceProbe reports, without reading the store or changing the state,
// what this state holds for addr's balance: its live object's balance, or
// the pending (unfolded) increase of an account it has not read. The
// parallel processor's store check (N42_MVS_CHECK) compares it across the
// apply walk.
func (sdb *IntraBlockState) BalanceProbe(addr types.Address) (bal uint256.Int, kind int) {
	if so, ok := sdb.stateObjects[addr]; ok && so != nil {
		return so.data.Balance, BalanceProbeObject
	}
	if bi, ok := sdb.balanceInc[addr]; ok && bi != nil && !bi.transferred {
		return bi.increase, BalanceProbePending
	}
	return bal, BalanceProbeNone
}
