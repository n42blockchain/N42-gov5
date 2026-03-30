// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// CachedPlainState wraps a PlainStateReader + PlainStateWriter with an
// in-memory write-through cache. All reads hit the cache first (O(1) map
// lookup), falling back to MDBX only on miss. Writes go to both cache
// and the underlying writer.
//
// This eliminates the MDBX B-tree IO bottleneck during replay: with 128GB
// RAM, the entire Account table (~720MB) fits in memory after the first pass.
type CachedPlainState struct {
	reader state.StateReader
	accts  map[types.Address]*cachedAccount
	store  map[storageKey][]byte
	code   map[types.Hash][]byte
}

type cachedAccount struct {
	data  *account.StateAccount
	empty bool // true = known to not exist
}

type storageKey struct {
	addr types.Address
	inc  uint16
	key  types.Hash
}

func NewCachedPlainState(reader state.StateReader) *CachedPlainState {
	return &CachedPlainState{
		reader: reader,
		accts:  make(map[types.Address]*cachedAccount, 1024*1024),
		store:  make(map[storageKey][]byte, 256*1024),
		code:   make(map[types.Hash][]byte, 1024),
	}
}

// --- StateReader implementation ---

func (c *CachedPlainState) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if ca, ok := c.accts[address]; ok {
		if ca.empty {
			return nil, nil
		}
		return ca.data.SelfCopy(), nil
	}
	// Cache miss: read from MDBX, cache the result.
	acct, err := c.reader.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		c.accts[address] = &cachedAccount{empty: true}
		return nil, nil
	}
	c.accts[address] = &cachedAccount{data: acct.SelfCopy()}
	return acct, nil
}

func (c *CachedPlainState) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	sk := storageKey{addr: address, inc: incarnation, key: *key}
	if v, ok := c.store[sk]; ok {
		if v == nil {
			return nil, nil
		}
		cp := make([]byte, len(v))
		copy(cp, v)
		return cp, nil
	}
	val, err := c.reader.ReadAccountStorage(address, incarnation, key)
	if err != nil {
		return nil, err
	}
	c.store[sk] = val
	return val, nil
}

func (c *CachedPlainState) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	if code, ok := c.code[codeHash]; ok {
		return code, nil
	}
	code, err := c.reader.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return nil, err
	}
	if code != nil {
		c.code[codeHash] = code
	}
	return code, nil
}

func (c *CachedPlainState) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	if code, ok := c.code[codeHash]; ok {
		return len(code), nil
	}
	return c.reader.ReadAccountCodeSize(address, incarnation, codeHash)
}

func (c *CachedPlainState) ReadAccountIncarnation(address types.Address) (uint16, error) {
	if ca, ok := c.accts[address]; ok && !ca.empty && ca.data != nil {
		return ca.data.Incarnation, nil
	}
	return c.reader.ReadAccountIncarnation(address)
}

// UpdateAccount caches account data after a write (write-through).
func (c *CachedPlainState) UpdateAccount(address types.Address, acct *account.StateAccount) {
	if acct == nil {
		c.accts[address] = &cachedAccount{empty: true}
	} else {
		c.accts[address] = &cachedAccount{data: acct.SelfCopy()}
	}
}

// UpdateStorage caches a storage value after a write.
func (c *CachedPlainState) UpdateStorage(address types.Address, incarnation uint16, key types.Hash, val []byte) {
	sk := storageKey{addr: address, inc: incarnation, key: key}
	if val == nil {
		c.store[sk] = nil
	} else {
		cp := make([]byte, len(val))
		copy(cp, val)
		c.store[sk] = cp
	}
}

// Len returns total cached entries (for monitoring).
func (c *CachedPlainState) Len() int {
	return len(c.accts) + len(c.store) + len(c.code)
}
