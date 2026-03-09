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

package snapshot

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/layered"
)

// DiskLayer is the bottom layer of the snapshot tree, backed by ShardedCache
// and ultimately MDBX. It does NOT perform DB reads itself — its Account and
// Storage methods always return (nil, false) to signal "not found in layer",
// so the SnapshotStateReader falls through to the inner StateReader for actual
// DB access. The ShardedCache is already integrated at the CachedStateReader
// level.
//
// The DiskLayer primarily serves as the terminator of the parent-chain walk
// and as a target for merging flattened diff layers into the cache.
type DiskLayer struct {
	cache    *layered.ShardedCache
	block    uint64
	root     types.Hash
	stale    atomic.Bool
}

// NewDiskLayer creates a new disk layer backed by the given cache.
func NewDiskLayer(cache *layered.ShardedCache, blockNum uint64, root types.Hash) *DiskLayer {
	return &DiskLayer{
		cache: cache,
		block: blockNum,
		root:  root,
	}
}

// Root returns the state root / block hash this layer was created at.
func (dl *DiskLayer) Root() types.Hash { return dl.root }

// BlockNumber returns the block number of this layer.
func (dl *DiskLayer) BlockNumber() uint64 { return dl.block }

// Parent returns nil — the disk layer has no parent.
func (dl *DiskLayer) Parent() Layer { return nil }

// Stale returns true if this disk layer has been superseded.
func (dl *DiskLayer) Stale() bool { return dl.stale.Load() }

// Account always returns (nil, false). Actual DB reads happen in the
// SnapshotStateReader's fallback path via the inner StateReader.
func (dl *DiskLayer) Account(_ types.Address) (*account.StateAccount, bool) {
	return nil, false
}

// Storage always returns (nil, false). Same rationale as Account.
func (dl *DiskLayer) Storage(_ types.Address, _ types.Hash) ([]byte, bool) {
	return nil, false
}

// AccountDeleted always returns false for the disk layer.
func (dl *DiskLayer) AccountDeleted(_ types.Address) bool {
	return false
}

// MarkStale marks this disk layer as superseded.
func (dl *DiskLayer) MarkStale() {
	dl.stale.Store(true)
}

// Cache returns the underlying ShardedCache.
func (dl *DiskLayer) Cache() *layered.ShardedCache {
	return dl.cache
}

// Compile-time check.
var _ Layer = (*DiskLayer)(nil)
