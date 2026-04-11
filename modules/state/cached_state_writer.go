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
// CachedStateWriter: WriterWithChangeSets wrapper keeping a cache coherent.
// UpdateAccountData forwards to the inner writer then Put's the
// encoded account into the layered.ShardedCache under modules.Account.
// UpdateAccountCode mirrors code writes into modules.Code. A nil cache
// degrades to a transparent pass-through for cheap opt-out.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
)

// CachedStateWriter wraps a WriterWithChangeSets and updates the cross-block
// ShardedCache on every state mutation, keeping the cache coherent with the
// latest committed state.
type CachedStateWriter struct {
	inner WriterWithChangeSets
	cache *layered.ShardedCache
}

// NewCachedStateWriter creates a CachedStateWriter that wraps inner with cache.
// If cache is nil, it behaves identically to inner.
func NewCachedStateWriter(inner WriterWithChangeSets, cache *layered.ShardedCache) *CachedStateWriter {
	return &CachedStateWriter{inner: inner, cache: cache}
}

func (w *CachedStateWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if err := w.inner.UpdateAccountData(address, original, acct); err != nil {
		return err
	}
	if w.cache != nil && acct != nil {
		w.cache.Put(modules.Account, address.Bytes(), acct.MarshalV2())
	}
	return nil
}

func (w *CachedStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	if err := w.inner.UpdateAccountCode(address, incarnation, codeHash, code); err != nil {
		return err
	}
	if w.cache != nil {
		w.cache.Put(modules.Code, codeHash[:], code)
	}
	return nil
}

func (w *CachedStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if err := w.inner.DeleteAccount(address, original); err != nil {
		return err
	}
	if w.cache != nil {
		w.cache.Delete(modules.Account, address.Bytes())
	}
	return nil
}

func (w *CachedStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if err := w.inner.WriteAccountStorage(address, incarnation, key, original, value); err != nil {
		return err
	}
	if w.cache != nil {
		compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
		v := value.Bytes()
		if len(v) == 0 {
			w.cache.Delete(modules.Storage, compositeKey)
		} else {
			w.cache.Put(modules.Storage, compositeKey, v)
		}
	}
	return nil
}

func (w *CachedStateWriter) CreateContract(address types.Address) error {
	return w.inner.CreateContract(address)
}

func (w *CachedStateWriter) WriteChangeSets() error {
	return w.inner.WriteChangeSets()
}

func (w *CachedStateWriter) WriteHistory() error {
	return w.inner.WriteHistory()
}

// Compile-time check.
var _ WriterWithChangeSets = (*CachedStateWriter)(nil)
