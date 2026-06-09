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
// BMTRootComputer: RootComputer adapter over a BMTCommitment.
// Implements state.RootComputer with RootScheme RootSchemeBMTBlake3.
// ComputeRoot turns the (accounts, storage) dirty maps into a single
// bmt.BatchEntry list for a top-down PutBatch traversal, avoiding
// intermediate root garbage from per-key Put calls.
// Deletions currently fall back to individual UpdateAccount calls.

package commitment

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// BMTRootComputer implements state.RootComputer using the BMT.
// It is the bridge between IntraBlockState's dirty-tracking and the BMT tree.
type BMTRootComputer struct {
	commitment *BMTCommitment
}

// NewBMTRootComputer wraps a BMTCommitment as a RootComputer.
func NewBMTRootComputer(c *BMTCommitment) *BMTRootComputer {
	return &BMTRootComputer{commitment: c}
}

// RootScheme reports that this root computer produces BMT/Blake3 state roots.
func (*BMTRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeBMTBlake3
}

// ComputeRoot applies all dirty accounts and storage to the BMT and returns
// the new root hash.
//
// It uses the sequential UpdateAccount/UpdateStorage (tree.Put/Delete) path,
// which is canonical: the resulting tree is a pure function of the live key set,
// independent of insertion order. The previous PutBatch path was NOT canonical —
// its "build from empty" branch collapses single-child nodes while its
// "update existing internal" branch keeps a single-child internal, so the two
// produce different structures for the same logical content, and a per-block
// incremental root diverges from a from-scratch rebuild. Root computation is a
// negligible fraction of replay time, so the per-key traversal is well worth the
// correctness.
func (r *BMTRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	for addr, acct := range accounts {
		if err := r.commitment.UpdateAccount(addr, acct); err != nil {
			return types.Hash{}, err
		}
	}
	for addr, slots := range storage {
		for slot, val := range slots {
			if err := r.commitment.UpdateStorage(addr, slot, val); err != nil {
				return types.Hash{}, err
			}
		}
	}
	return r.commitment.Root(), nil
}
