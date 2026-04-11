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
// Layer interface for nodes in the snapshot acceleration tree.
// Root, BlockNumber and Parent describe tree topology; Stale flags
// reorg invalidation. Account and Storage return an explicit
// (value, found) pair where found=false means the layer chain has
// no information, and a nil value with found=true represents an
// intentional deletion in one of the layers.

package snapshot

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// Layer is a state layer in the snapshot acceleration tree.
// The tree consists of a bottom DiskLayer backed by ShardedCache + MDBX,
// and zero or more DiffLayers stacked on top, each representing a single
// block's state mutations. Lookups walk the layer chain from newest to oldest.
type Layer interface {
	// Root returns the state root hash (block hash) this layer represents.
	Root() types.Hash

	// BlockNumber returns the block number of this layer.
	BlockNumber() uint64

	// Parent returns the parent layer, or nil for the disk layer.
	Parent() Layer

	// Stale returns true if this layer has been invalidated (e.g., by reorg).
	Stale() bool

	// Account returns the account at the given address.
	// Returns (account, true) if found in this layer or its ancestors,
	// or (nil, false) if the layer chain does not contain information about
	// this address (i.e., the caller should fall through to the DB reader).
	Account(addr types.Address) (*account.StateAccount, bool)

	// Storage returns storage value at given address+key.
	// Returns (value, true) if found (value may be nil for deleted slots),
	// or (nil, false) if the layer chain has no information.
	Storage(addr types.Address, key types.Hash) ([]byte, bool)

	// AccountDeleted returns true if the account was explicitly deleted in
	// this layer (not ancestors). Used to stop upward traversal.
	AccountDeleted(addr types.Address) bool
}
