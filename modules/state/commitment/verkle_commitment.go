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
// VerkleCommitment bridges dirty-tracking to a go-verkle tree.
// Wraps a gverkle.VerkleNode root plus a libverkle.NodeStore with a
// Resolver for lazy node loading. UpdateAccount/UpdateStorage apply
// per-block diffs, Root commits and returns the 32-byte state hash,
// and Flush persists new nodes to the backing store. Runs in
// parallel with PlainStateWriter like the JMT/BMT commitments.

package commitment

import (
	gverkle "github.com/ethereum/go-verkle"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	libverkle "github.com/n42blockchain/N42/lib/verkle"
)

// VerkleCommitment bridges IntraBlockState dirty-tracking with a Verkle tree.
//
// Usage per block:
//  1. Create or reuse a VerkleCommitment (backed by the same NodeStore).
//  2. For each dirty account: call UpdateAccount.
//  3. For each dirty storage slot: call UpdateStorage.
//  4. Call Root() to get the new state root.
//  5. Call Flush() to persist all new Verkle nodes to the backing store.
type VerkleCommitment struct {
	root     gverkle.VerkleNode
	store    libverkle.NodeStore
	resolver gverkle.NodeResolverFn
}

// NewVerkleCommitment creates a commitment layer around the given Verkle tree root
// and backing store. The resolver is derived from the store for lazy node loading.
func NewVerkleCommitment(root gverkle.VerkleNode, store libverkle.NodeStore) *VerkleCommitment {
	return &VerkleCommitment{
		root:     root,
		store:    store,
		resolver: libverkle.Resolver(store),
	}
}

// InternalRoot returns the underlying go-verkle tree root.
func (c *VerkleCommitment) InternalRoot() gverkle.VerkleNode {
	return c.root
}

// Root commits the tree and returns the 32-byte state root hash.
func (c *VerkleCommitment) Root() types.Hash {
	point := c.root.Commit()
	if point == nil {
		return types.Hash{}
	}
	raw := point.Bytes()
	var h types.Hash
	copy(h[:], raw[:32])
	return h
}

// Flush serializes all non-hashed nodes and writes them to the backing store.
// Returns the number of new nodes written and total bytes.
func (c *VerkleCommitment) Flush() (int, int64, error) {
	return libverkle.Flush(c.root, c.store)
}

// FlushTo serializes all non-hashed nodes and writes them to the given target
// store. Used for per-block persistence to MDBX within a write transaction.
func (c *VerkleCommitment) FlushTo(target libverkle.NodeStore) (int, int64, error) {
	return libverkle.Flush(c.root, target)
}

// UpdateAccount inserts or updates an account in the Verkle tree.
// If the account is nil or empty, it is deleted.
func (c *VerkleCommitment) UpdateAccount(addr types.Address, acct *account.StateAccount) error {
	keyHash := AccountKeyHash(addr)

	if acct == nil || isAccountEmpty(acct) {
		_, err := c.root.Delete(keyHash[:], c.resolver)
		return err
	}

	val := EncodeAccountValue(acct)
	// go-verkle accepts variable-length values. Pad to 32 bytes minimum
	// (required by the IPA commitment scheme).
	if len(val) < 32 {
		var vval [32]byte
		copy(vval[32-len(val):], val)
		return c.root.Insert(keyHash[:], vval[:], c.resolver)
	}
	return c.root.Insert(keyHash[:], val, c.resolver)
}

// UpdateStorage inserts, updates, or deletes a storage slot in the Verkle tree.
func (c *VerkleCommitment) UpdateStorage(addr types.Address, slot types.Hash, value *uint256.Int) error {
	keyHash := StorageKeyHash(addr, slot)

	if value == nil || value.IsZero() {
		_, err := c.root.Delete(keyHash[:], c.resolver)
		return err
	}

	b := value.Bytes32()
	return c.root.Insert(keyHash[:], b[:], c.resolver)
}
