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
// BMTCommitment bridges dirty-tracking to a Binary Merkle Tree.
// Wraps a lib/bmt.Tree and exposes UpdateAccount/UpdateStorage/Root
// entrypoints used from IntermediateRoot.
// Does not replace PlainStateWriter — flat tables are still written
// in parallel so point lookups remain fast and only the cryptographic
// root is produced via the BMT commitment layer.

package commitment

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/bmt"
)

// BMTCommitment bridges the IntraBlockState dirty-tracking with the BMT.
//
// Usage per block:
//  1. Create or reuse a BMTCommitment (backed by the same NodeStore).
//  2. For each dirty account: call UpdateAccount.
//  3. For each dirty storage slot: call UpdateStorage.
//  4. Call Root() to get the new state root.
//  5. Call tree.FlushTo(store) to persist all new BMT nodes to an external store.
//
// The BMTCommitment does NOT replace PlainStateWriter — the flat Account/
// Storage tables are still written in parallel for fast point lookups.
type BMTCommitment struct {
	tree *bmt.Tree
}

// NewBMTCommitment creates a commitment layer around the given BMT tree.
func NewBMTCommitment(tree *bmt.Tree) *BMTCommitment {
	return &BMTCommitment{tree: tree}
}

// Tree returns the underlying BMT tree (used for flush-to-external-store).
func (c *BMTCommitment) Tree() *bmt.Tree {
	return c.tree
}

// Root returns the current BMT state root hash.
func (c *BMTCommitment) Root() types.Hash {
	return c.tree.Root()
}

// UpdateAccount inserts or updates an account in the BMT.
// If the account is nil or empty (Nonce==0, Balance==0, not Initialised),
// it is deleted.
func (c *BMTCommitment) UpdateAccount(addr types.Address, acct *account.StateAccount) error {
	keyHash := AccountKeyHash(addr)
	bmtKey := bmt.Hash(keyHash)

	if acct == nil || isAccountEmpty(acct) {
		err := c.tree.Delete(bmtKey)
		if err == bmt.ErrNotFound {
			return nil
		}
		return err
	}

	return c.tree.Put(bmtKey, EncodeAccountValue(acct))
}

// UpdateStorage inserts, updates, or deletes a storage slot in the BMT.
// A nil or zero value means deletion. Non-zero values are stored with
// leading zero bytes stripped for compactness.
func (c *BMTCommitment) UpdateStorage(addr types.Address, slot types.Hash, value *uint256.Int) error {
	keyHash := StorageKeyHash(addr, slot)
	bmtKey := bmt.Hash(keyHash)

	if value == nil || value.IsZero() {
		err := c.tree.Delete(bmtKey)
		if err == bmt.ErrNotFound {
			return nil
		}
		return err
	}

	b := value.Bytes32()
	start := 0
	for start < 31 && b[start] == 0 {
		start++
	}
	return c.tree.Put(bmtKey, b[start:])
}
