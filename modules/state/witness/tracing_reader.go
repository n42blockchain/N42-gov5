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

package witness

import (
	"sync"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// TracingReader wraps a state.StateReader and records all keys that are accessed
// during EVM execution. The recorded access set is later used by the Generator
// to produce a minimal BlockWitness containing only the required proofs.
type TracingReader struct {
	inner state.StateReader

	mu       sync.Mutex
	accounts map[types.Address]bool
	storage  map[types.Address]map[types.Hash]bool
	codes    map[types.Address]bool
}

// Compile-time interface check.
var _ state.StateReader = (*TracingReader)(nil)

// NewTracingReader creates a TracingReader that delegates to inner and
// records all state keys accessed through the StateReader interface.
// Panics if inner is nil.
func NewTracingReader(inner state.StateReader) *TracingReader {
	if inner == nil {
		panic("witness: NewTracingReader called with nil StateReader")
	}
	return &TracingReader{
		inner:    inner,
		accounts: make(map[types.Address]bool),
		storage:  make(map[types.Address]map[types.Hash]bool),
		codes:    make(map[types.Address]bool),
	}
}

// ReadAccountData delegates to the inner reader and records the account address.
func (tr *TracingReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	tr.mu.Lock()
	tr.accounts[address] = true
	tr.mu.Unlock()

	return tr.inner.ReadAccountData(address)
}

// ReadAccountStorage delegates to the inner reader and records the storage key.
func (tr *TracingReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	tr.mu.Lock()
	tr.accounts[address] = true
	if key != nil {
		slots, ok := tr.storage[address]
		if !ok {
			slots = make(map[types.Hash]bool)
			tr.storage[address] = slots
		}
		slots[*key] = true
	}
	tr.mu.Unlock()

	return tr.inner.ReadAccountStorage(address, incarnation, key)
}

// ReadAccountCode delegates to the inner reader and records the code access.
func (tr *TracingReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	tr.mu.Lock()
	tr.accounts[address] = true
	tr.codes[address] = true
	tr.mu.Unlock()

	return tr.inner.ReadAccountCode(address, incarnation, codeHash)
}

// ReadAccountCodeSize delegates to the inner reader and records the code access.
func (tr *TracingReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	tr.mu.Lock()
	tr.accounts[address] = true
	tr.codes[address] = true
	tr.mu.Unlock()

	return tr.inner.ReadAccountCodeSize(address, incarnation, codeHash)
}

// ReadAccountIncarnation delegates to the inner reader and records the account address.
func (tr *TracingReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	tr.mu.Lock()
	tr.accounts[address] = true
	tr.mu.Unlock()

	return tr.inner.ReadAccountIncarnation(address)
}

// AccessedAccounts returns a deduplicated list of all account addresses
// that were accessed through this reader.
func (tr *TracingReader) AccessedAccounts() []types.Address {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	addrs := make([]types.Address, 0, len(tr.accounts))
	for addr := range tr.accounts {
		addrs = append(addrs, addr)
	}
	return addrs
}

// AccessedStorage returns a map of address to a slice of storage slots
// that were accessed through this reader.
func (tr *TracingReader) AccessedStorage() map[types.Address][]types.Hash {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	result := make(map[types.Address][]types.Hash, len(tr.storage))
	for addr, slots := range tr.storage {
		keys := make([]types.Hash, 0, len(slots))
		for slot := range slots {
			keys = append(keys, slot)
		}
		result[addr] = keys
	}
	return result
}

// AccessedCodes returns a deduplicated list of all addresses whose
// contract code was accessed through this reader.
func (tr *TracingReader) AccessedCodes() []types.Address {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	addrs := make([]types.Address, 0, len(tr.codes))
	for addr := range tr.codes {
		addrs = append(addrs, addr)
	}
	return addrs
}
