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

package state

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// transientStorage is an EIP-1153 implementation of transient storage.
// Transient storage is cleared at the end of each transaction.
type transientStorage map[types.Address]Storage

// newTransientStorage creates a new transient storage instance.
func newTransientStorage() transientStorage {
	return make(transientStorage)
}

// Set stores a value in transient storage.
func (t transientStorage) Set(addr types.Address, key types.Hash, value uint256.Int) {
	if _, ok := t[addr]; !ok {
		t[addr] = make(Storage)
	}
	t[addr][key] = value
}

// Get retrieves a value from transient storage.
func (t transientStorage) Get(addr types.Address, key types.Hash) uint256.Int {
	val, ok := t[addr]
	if !ok {
		return uint256.Int{}
	}
	return val[key]
}

// Copy creates a deep copy of the transient storage.
func (t transientStorage) Copy() transientStorage {
	cp := make(transientStorage, len(t))
	for addr, storage := range t {
		cp[addr] = make(Storage, len(storage))
		for key, val := range storage {
			cp[addr][key] = val
		}
	}
	return cp
}

