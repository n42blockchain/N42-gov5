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
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// DiffLayer holds the state diffs produced by executing a single block.
// It forms a singly-linked chain: each DiffLayer points to its parent,
// which is either another DiffLayer or the bottom DiskLayer.
//
// Lookups traverse the chain from newest to oldest. A nil entry in the
// accounts map means the account was deleted (distinguished from "not
// touched in this layer" which is simply absent from the map).
type DiffLayer struct {
	parent Layer
	root   types.Hash
	block  uint64

	// accounts holds accounts that were modified in this block.
	// A nil value is NOT used for deletion — deleted accounts are tracked
	// separately in accountDels to avoid ambiguity with "not present".
	accounts    map[types.Address]*account.StateAccount
	accountDels map[types.Address]struct{} // explicitly deleted accounts
	storage     map[types.Address]map[types.Hash][]byte

	lock   sync.RWMutex
	stale  atomic.Bool
	memory uint64 // approximate memory usage in bytes
}

// NewDiffLayer creates a new diff layer on top of parent.
// The caller must NOT modify the passed maps after this call.
func NewDiffLayer(
	parent Layer,
	blockNum uint64,
	root types.Hash,
	accounts map[types.Address]*account.StateAccount,
	accountDels map[types.Address]struct{},
	storage map[types.Address]map[types.Hash][]byte,
) *DiffLayer {
	dl := &DiffLayer{
		parent:      parent,
		root:        root,
		block:       blockNum,
		accounts:    accounts,
		accountDels: accountDels,
		storage:     storage,
	}
	dl.memory = dl.estimateMemory()
	return dl
}

// Root returns the block hash / state root this layer represents.
func (dl *DiffLayer) Root() types.Hash { return dl.root }

// BlockNumber returns the block number of this layer.
func (dl *DiffLayer) BlockNumber() uint64 { return dl.block }

// Parent returns the parent layer.
func (dl *DiffLayer) Parent() Layer { return dl.parent }

// Stale returns true if this layer has been invalidated.
func (dl *DiffLayer) Stale() bool { return dl.stale.Load() }

// MarkStale marks this layer and all its children as invalid.
// Called during reorg when the block this layer represents is no longer canonical.
func (dl *DiffLayer) MarkStale() {
	dl.stale.Store(true)
}

// Account looks up an account in this layer, then walks up the parent chain.
// Returns (nil, true) if the account was explicitly deleted.
// Returns (nil, false) if the layer chain has no information about this address.
func (dl *DiffLayer) Account(addr types.Address) (*account.StateAccount, bool) {
	if dl.stale.Load() {
		return nil, false
	}

	dl.lock.RLock()
	// Check deletion first.
	if _, deleted := dl.accountDels[addr]; deleted {
		dl.lock.RUnlock()
		return nil, true
	}
	// Check modification.
	if acc, ok := dl.accounts[addr]; ok {
		dl.lock.RUnlock()
		return acc, true
	}
	parent := dl.parent
	dl.lock.RUnlock()

	// Not in this layer — ask parent.
	if parent != nil {
		return parent.Account(addr)
	}
	return nil, false
}

// Storage looks up a storage slot in this layer, then walks up the parent chain.
// Returns (nil, true) if the slot was explicitly zeroed.
// Returns (nil, false) if the layer chain has no information about this slot.
func (dl *DiffLayer) Storage(addr types.Address, key types.Hash) ([]byte, bool) {
	if dl.stale.Load() {
		return nil, false
	}

	dl.lock.RLock()
	// If the account was deleted in this layer, storage is gone too.
	if _, deleted := dl.accountDels[addr]; deleted {
		dl.lock.RUnlock()
		return nil, true
	}
	if slots, ok := dl.storage[addr]; ok {
		if val, ok := slots[key]; ok {
			dl.lock.RUnlock()
			return val, true
		}
	}
	parent := dl.parent
	dl.lock.RUnlock()

	if parent != nil {
		return parent.Storage(addr, key)
	}
	return nil, false
}

// AccountDeleted returns true if the account was explicitly deleted in this
// specific layer (does not check parent layers).
func (dl *DiffLayer) AccountDeleted(addr types.Address) bool {
	dl.lock.RLock()
	defer dl.lock.RUnlock()
	_, deleted := dl.accountDels[addr]
	return deleted
}

// Memory returns the approximate memory consumption of this single layer.
func (dl *DiffLayer) Memory() uint64 { return dl.memory }

// estimateMemory returns a rough byte count of the data stored in this layer.
func (dl *DiffLayer) estimateMemory() uint64 {
	var mem uint64
	// Each account entry: address (20) + pointer (8) + StateAccount struct (~120)
	mem += uint64(len(dl.accounts)) * (uint64(unsafe.Sizeof(types.Address{})) + 128)
	// Each deletion entry: address (20) + struct{} (0)
	mem += uint64(len(dl.accountDels)) * uint64(unsafe.Sizeof(types.Address{}))
	// Storage: address (20) + inner map overhead + entries
	for _, slots := range dl.storage {
		mem += uint64(unsafe.Sizeof(types.Address{})) + 64 // map overhead
		for _, v := range slots {
			mem += uint64(types.HashLength) + uint64(len(v)) + 16 // key + value + overhead
		}
	}
	return mem
}

// Compile-time check.
var _ Layer = (*DiffLayer)(nil)
