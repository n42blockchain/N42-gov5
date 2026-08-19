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
// CachedStateReader: StateReader wrapper backed by a cross-block cache.
// NewCachedStateReader composes an inner StateReader with a
// layered.ShardedCache. ReadAccountData checks modules.Account keys in
// the cache before falling back to the inner reader, decoding with
// account.DecodeForStorage and evicting corrupted entries on decode
// failure. A nil cache reduces overhead to the plain inner reader.

package state

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
)

// CachedStateReader wraps a StateReader with a cross-block ShardedCache.
// It accelerates repeated reads of the same account/storage/code data
// across multiple blocks, complementing IntraBlockState's per-block cache.
//
// Cache keys are table+dbKey (same as LayeredDB), so the cache is shared
// with the DB layer for maximum hit rate.
type CachedStateReader struct {
	inner StateReader
	cache *layered.ShardedCache
}

// NewCachedStateReader creates a CachedStateReader that wraps inner with cache.
// If cache is nil, it behaves identically to inner (no overhead).
func NewCachedStateReader(inner StateReader, cache *layered.ShardedCache) *CachedStateReader {
	return &CachedStateReader{inner: inner, cache: cache}
}

func (r *CachedStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	key := address.Bytes()

	// Check cache.
	if r.cache != nil {
		if enc, ok := r.cache.Get(modules.Account, key); ok {
			if len(enc) == 0 {
				return nil, nil
			}
			var a account.StateAccount
			if err := a.DecodeForStorage(enc); err != nil {
				// Cache entry corrupted — fall through to DB.
				r.cache.Delete(modules.Account, key)
			} else {
				return &a, nil
			}
		}
	}

	// Cache miss — read from underlying reader.
	a, err := r.inner.ReadAccountData(address)
	if err != nil {
		return nil, err
	}

	// Populate cache. Store the same EncodeForStorage format that the read path expects.
	if r.cache != nil {
		if a == nil {
			r.cache.Put(modules.Account, key, nil) // negative cache
		} else {
			enc := make([]byte, a.EncodingLengthForStorage())
			a.EncodeForStorage(enc)
			r.cache.Put(modules.Account, key, enc)
		}
	}

	return a, nil
}

func (r *CachedStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())

	if r.cache != nil {
		if v, ok := r.cache.Get(modules.Storage, compositeKey); ok {
			if len(v) == 0 {
				return nil, nil
			}
			return v, nil
		}
	}

	enc, err := r.inner.ReadAccountStorage(address, key)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		r.cache.Put(modules.Storage, compositeKey, enc)
	}

	return enc, nil
}

func (r *CachedStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	cacheKey := codeHash[:]

	if r.cache != nil {
		if v, ok := r.cache.Get(modules.Code, cacheKey); ok {
			if len(v) == 0 {
				return nil, nil
			}
			return v, nil
		}
	}

	code, err := r.inner.ReadAccountCode(address, codeHash)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		r.cache.Put(modules.Code, cacheKey, code)
	}

	return code, nil
}

func (r *CachedStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}

// ForEachStorage delegates to the inner reader. The flat ShardedCache cannot
// be enumerated by address prefix, and a full slot scan must reflect the
// authoritative DB state anyway, so this bypasses the cache entirely and
// forwards to inner when inner supports StorageEnumerator.
func (r *CachedStateReader) ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error {
	if enum, ok := r.inner.(StorageEnumerator); ok {
		return enum.ForEachStorage(addr, f)
	}
	// This type declares StorageEnumerator unconditionally, so an inner
	// reader that cannot enumerate must be reported, not answered with a
	// silent empty scan — see ErrNoStorageEnumeration.
	return ErrNoStorageEnumeration
}

// Compile-time check.
var (
	_ StateReader       = (*CachedStateReader)(nil)
	_ StorageEnumerator = (*CachedStateReader)(nil)
)
