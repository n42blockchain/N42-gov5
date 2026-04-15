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
// SnapshotStateReader: Layer-first StateReader adapter.
// Consults a snapshot Layer chain for accounts and storage before
// falling back to an inner state.StateReader (CachedStateReader or
// PlainStateReader). When the Layer reports an explicit deletion it
// returns (nil, nil) without descending to the inner reader,
// providing sub-millisecond reads for recently modified state.

package snapshot

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// SnapshotStateReader wraps a Layer to implement state.StateReader.
// For every read, it first consults the snapshot layer chain (diff layers)
// and falls back to the inner reader (typically a CachedStateReader or
// PlainStateReader) for data not covered by any diff layer.
//
// This provides sub-millisecond reads for recently modified state without
// requiring a full DB transaction.
type SnapshotStateReader struct {
	layer Layer
	inner state.StateReader
}

// NewSnapshotStateReader creates a new reader that checks layer first,
// then falls back to inner.
func NewSnapshotStateReader(layer Layer, inner state.StateReader) *SnapshotStateReader {
	return &SnapshotStateReader{
		layer: layer,
		inner: inner,
	}
}

// ReadAccountData reads the account from the snapshot layer chain.
// If the layer chain has no information, falls back to the inner reader.
// If the layer says the account was deleted, returns (nil, nil).
func (r *SnapshotStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.layer != nil && !r.layer.Stale() {
		if acc, found := r.layer.Account(address); found {
			snapshotLayerHits.Inc()
			return acc, nil // acc may be nil if account was deleted
		}
		snapshotLayerMisses.Inc()
	}
	return r.inner.ReadAccountData(address)
}

// ReadAccountStorage reads a storage slot from the snapshot layer chain.
// Falls back to the inner reader if the layer chain has no information.
func (r *SnapshotStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if r.layer != nil && !r.layer.Stale() {
		if val, found := r.layer.Storage(address, *key); found {
			snapshotLayerHits.Inc()
			return val, nil
		}
		snapshotLayerMisses.Inc()
	}
	return r.inner.ReadAccountStorage(address, key)
}

// ReadAccountCode reads contract code. Diff layers do not store code,
// so this always delegates to the inner reader.
func (r *SnapshotStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	return r.inner.ReadAccountCode(address, codeHash)
}

// ReadAccountCodeSize returns the size of contract code.
// Delegates to inner reader since diff layers don't store code.
func (r *SnapshotStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	return r.inner.ReadAccountCodeSize(address, codeHash)
}

// Compile-time check.
var _ state.StateReader = (*SnapshotStateReader)(nil)
