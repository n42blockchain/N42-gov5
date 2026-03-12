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
)

// JMTCommitment bridges the IntraBlockState dirty-tracking with the JMT.
//
// Usage per block:
//  1. Create or reuse a JMTCommitment (backed by the same NodeStore).
//  2. For each dirty account: call UpdateAccount.
//  3. For each dirty storage slot: call UpdateStorage.
//  4. Call Root() to get the new state root.
//  5. Call FlushTo(store) to persist all new JMT nodes to an external store.
//
// The JMTCommitment does NOT replace PlainStateWriter — the flat Account/
// Storage tables are still written in parallel for fast point lookups.
type JMTCommitment struct {
	tree *jmt.Tree
}

// NewJMTCommitment creates a commitment layer around the given JMT tree.
func NewJMTCommitment(tree *jmt.Tree) *JMTCommitment {
	return &JMTCommitment{tree: tree}
}

// Tree returns the underlying JMT tree (used for flush-to-external-store).
func (c *JMTCommitment) Tree() *jmt.Tree {
	return c.tree
}

// UpdateAccount inserts or updates an account in the JMT.
// If the account is empty (Nonce==0, Balance==0, no code), it is deleted.
func (c *JMTCommitment) UpdateAccount(addr types.Address, acct *account.StateAccount) error {
	keyHash := AccountKeyHash(addr)

	if isAccountEmpty(acct) {
		err := c.tree.Delete(keyHash)
		if err == jmt.ErrNotFound {
			return nil
		}
		return err
	}

	value := EncodeAccountValue(acct)
	return c.tree.Put(keyHash, value)
}

// DeleteAccount removes an account from the JMT.
func (c *JMTCommitment) DeleteAccount(addr types.Address) error {
	keyHash := AccountKeyHash(addr)
	err := c.tree.Delete(keyHash)
	if err == jmt.ErrNotFound {
		return nil
	}
	return err
}

// UpdateStorage inserts, updates, or deletes a storage slot in the JMT.
// A zero value means deletion.
func (c *JMTCommitment) UpdateStorage(addr types.Address, slot types.Hash, value *uint256.Int) error {
	keyHash := StorageKeyHash(addr, slot)

	if value == nil || value.IsZero() {
		err := c.tree.Delete(keyHash)
		if err == jmt.ErrNotFound {
			return nil
		}
		return err
	}

	var buf [32]byte
	value.WriteToSlice(buf[:])
	return c.tree.Put(keyHash, buf[:])
}

// Root returns the current JMT state root hash.
func (c *JMTCommitment) Root() types.Hash {
	jmtRoot := c.tree.Root()
	var h types.Hash
	copy(h[:], jmtRoot[:])
	return h
}

// Flush persists all dirty JMT nodes to the backing NodeStore.
func (c *JMTCommitment) Flush() error {
	return c.tree.Flush()
}

// DirtyNodeCount returns the number of unflushed JMT nodes.
func (c *JMTCommitment) DirtyNodeCount() int {
	return c.tree.DirtyCount()
}

// GetAccountProof generates a Merkle inclusion/exclusion proof for an account.
func (c *JMTCommitment) GetAccountProof(addr types.Address) (*jmt.Proof, error) {
	keyHash := AccountKeyHash(addr)
	return c.tree.GetProof(keyHash)
}

// GetStorageProof generates a Merkle proof for a storage slot.
func (c *JMTCommitment) GetStorageProof(addr types.Address, slot types.Hash) (*jmt.Proof, error) {
	keyHash := StorageKeyHash(addr, slot)
	return c.tree.GetProof(keyHash)
}

// isAccountEmpty checks if an account should be pruned from the JMT.
func isAccountEmpty(a *account.StateAccount) bool {
	return a.Nonce == 0 && a.Balance.IsZero() && !a.Initialised
}
