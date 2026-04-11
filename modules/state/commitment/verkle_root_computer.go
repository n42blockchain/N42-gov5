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
// VerkleRootComputer: RootComputer adapter over a VerkleCommitment.
// Implements state.RootComputer with RootScheme RootSchemeVerkle.
// ComputeRoot iterates dirty accounts and storage and forwards each
// mutation to the underlying VerkleCommitment.UpdateAccount/
// UpdateStorage calls before returning the committed tree root.

package commitment

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// VerkleRootComputer implements state.RootComputer using the Verkle tree.
type VerkleRootComputer struct {
	commitment *VerkleCommitment
}

// NewVerkleRootComputer wraps a VerkleCommitment as a RootComputer.
func NewVerkleRootComputer(c *VerkleCommitment) *VerkleRootComputer {
	return &VerkleRootComputer{commitment: c}
}

// RootScheme reports that this root computer produces Verkle state roots.
func (*VerkleRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeVerkle
}

// ComputeRoot applies all dirty accounts and storage to the Verkle tree
// and returns the new root hash.
func (r *VerkleRootComputer) ComputeRoot(
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
