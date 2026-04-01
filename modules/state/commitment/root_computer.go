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

package commitment

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
)

// JMTRootComputer implements state.RootComputer using the JMT.
// It is the bridge between IntraBlockState's dirty-tracking and the JMT tree.
type JMTRootComputer struct {
	commitment *JMTCommitment
}

// NewJMTRootComputer wraps a JMTCommitment as a RootComputer.
func NewJMTRootComputer(c *JMTCommitment) *JMTRootComputer {
	return &JMTRootComputer{commitment: c}
}

// RootScheme reports that this root computer produces JMT/Blake3 state roots.
func (*JMTRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeJMTBlake3
}

// ComputeRoot applies all dirty accounts and storage to the JMT and returns
// the new root hash. This is called once per block from IntermediateRoot().
func (r *JMTRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	// Build a batch of all mutations.
	entries := make([]jmt.BatchEntry, 0, len(accounts)+countStorageSlots(storage))

	for addr, acct := range accounts {
		keyHash := AccountKeyHash(addr)
		if acct == nil || isAccountEmpty(acct) {
			entries = append(entries, jmt.BatchEntry{KeyHash: keyHash, Value: nil})
		} else {
			entries = append(entries, jmt.BatchEntry{KeyHash: keyHash, Value: EncodeAccountValue(acct)})
		}
	}

	for addr, slots := range storage {
		for slot, val := range slots {
			keyHash := StorageKeyHash(addr, slot)
			if val == nil || val.IsZero() {
				entries = append(entries, jmt.BatchEntry{KeyHash: keyHash, Value: nil})
			} else {
				var buf [32]byte
				val.WriteToSlice(buf[:])
				entries = append(entries, jmt.BatchEntry{KeyHash: keyHash, Value: buf[:]})
			}
		}
	}

	root, err := r.commitment.tree.BatchUpdate(entries)
	if err != nil {
		return types.Hash{}, err
	}

	var h types.Hash
	copy(h[:], root[:])
	return h, nil
}

func countStorageSlots(storage map[types.Address]map[types.Hash]*uint256.Int) int {
	n := 0
	for _, slots := range storage {
		n += len(slots)
	}
	return n
}
