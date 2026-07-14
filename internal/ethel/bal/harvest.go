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
//
// EIP-7928 Phase 2: harvest per-tx access records from the block-STM execution
// views. Each committed MVStateView already carries this tx's read set (every
// slot/account it touched) and write set (post-execution values keyed by the
// MVHashMap composite key), so a BlockAccessList can be assembled as a read-only
// side-effect of parallel execution — no re-execution, no consensus coupling.

package bal

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// AccessView is the read-only surface of a committed block-STM tx view.
// *state.MVStateView satisfies it; tests supply lightweight fakes.
type AccessView interface {
	ReadSet() []state.ReadRecord
	WriteSet() map[string][]byte
}

// HarvestTxAccess turns one committed tx view into an EIP-7928 access record.
// Storage post-values come straight from the write set; balance and nonce are
// decoded from the account write (an account write means something on it changed,
// so its post balance/nonce are reported — strict change-vs-prestate filtering
// and code-body reporting are deferred to a later phase). Reads to slots this tx
// also wrote are reconciled away by BuildBAL, not here.
func HarvestTxAccess(txIndex uint16, v AccessView) TxAccess {
	ta := TxAccess{TxIndex: txIndex}

	for k, val := range v.WriteSet() {
		key := []byte(k)
		if addr, slot, ok := state.DecodeStorageKey(key); ok {
			// nil / empty value == slot written to zero: still a post-value of 0.
			nv := types.Hash(new(uint256.Int).SetBytes(val).Bytes32())
			ta.StorageWrites = append(ta.StorageWrites, SlotWrite{Address: addr, Slot: slot, NewValue: nv})
			continue
		}
		if addr, ok := state.DecodeAccountKey(key); ok {
			if val == nil {
				continue // account deleted this tx; represented by absence in phase 2
			}
			var acc account.StateAccount
			if err := acc.DecodeForStorageV2(val); err != nil {
				continue
			}
			ta.BalanceChanges = append(ta.BalanceChanges, AccountBalance{Address: addr, PostBalance: acc.Balance})
			ta.NonceChanges = append(ta.NonceChanges, AccountNonce{Address: addr, NewNonce: acc.Nonce})
			continue
		}
		// code / wipe keys: not part of the phase-2 harvest.
	}

	for _, rr := range v.ReadSet() {
		if addr, slot, ok := state.DecodeStorageKey(rr.Key); ok {
			ta.StorageReads = append(ta.StorageReads, SlotRead{Address: addr, Slot: slot})
		}
	}

	return ta
}

// BuildBALFromViews harvests every committed tx view and folds them into a
// canonical BlockAccessList. baseTxIndex is the EIP-7928 index assigned to
// views[0] (1 when views are the block's transactions and index 0 is reserved
// for pre-block system calls); subsequent views increment from it.
func BuildBALFromViews(views []AccessView, baseTxIndex uint16) *BlockAccessList {
	txs := make([]TxAccess, 0, len(views))
	for i, v := range views {
		txs = append(txs, HarvestTxAccess(baseTxIndex+uint16(i), v))
	}
	return BuildBAL(txs)
}
