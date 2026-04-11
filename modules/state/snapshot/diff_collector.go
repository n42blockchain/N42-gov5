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
// DiffCollector intercepts writes to build snapshot DiffLayers.
// Wraps a state.WriterWithChangeSets and records UpdateAccountData,
// DeleteAccount and storage writes into parallel accounts/accountDels
// /storage maps while forwarding to the inner writer unchanged.
// After CommitBlock the captured maps are promoted into a new
// DiffLayer added to the snapshot Tree.

package snapshot

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// DiffCollector wraps a WriterWithChangeSets and collects state diffs
// as they are written during block execution. After CommitBlock completes,
// the collected diffs can be used to create a DiffLayer.
//
// This approach avoids modifying IntraBlockState internals — it intercepts
// writes at the StateWriter level, which is the natural integration point.
type DiffCollector struct {
	inner       state.WriterWithChangeSets
	accounts    map[types.Address]*account.StateAccount
	accountDels map[types.Address]struct{}
	storage     map[types.Address]map[types.Hash][]byte
}

// NewDiffCollector wraps the given writer and captures all state mutations.
func NewDiffCollector(inner state.WriterWithChangeSets) *DiffCollector {
	return &DiffCollector{
		inner:       inner,
		accounts:    make(map[types.Address]*account.StateAccount),
		accountDels: make(map[types.Address]struct{}),
		storage:     make(map[types.Address]map[types.Hash][]byte),
	}
}

// UpdateAccountData records an account update and delegates to inner.
func (dc *DiffCollector) UpdateAccountData(address types.Address, original, acc *account.StateAccount) error {
	if acc != nil {
		dc.accounts[address] = acc.SelfCopy()
	}
	return dc.inner.UpdateAccountData(address, original, acc)
}

// UpdateAccountCode delegates to inner. Code is not tracked in diff layers
// since it is immutable (content-addressed by hash).
func (dc *DiffCollector) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	return dc.inner.UpdateAccountCode(address, incarnation, codeHash, code)
}

// DeleteAccount records an account deletion and delegates to inner.
func (dc *DiffCollector) DeleteAccount(address types.Address, original *account.StateAccount) error {
	dc.accountDels[address] = struct{}{}
	delete(dc.accounts, address) // Remove any prior update in same block.
	return dc.inner.DeleteAccount(address, original)
}

// WriteAccountStorage records a storage write and delegates to inner.
func (dc *DiffCollector) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	slots, ok := dc.storage[address]
	if !ok {
		slots = make(map[types.Hash][]byte)
		dc.storage[address] = slots
	}
	if value == nil || value.IsZero() {
		slots[*key] = nil
	} else {
		slots[*key] = value.Bytes()
	}
	return dc.inner.WriteAccountStorage(address, incarnation, key, original, value)
}

// CreateContract delegates to inner.
func (dc *DiffCollector) CreateContract(address types.Address) error {
	return dc.inner.CreateContract(address)
}

// WriteChangeSets delegates to inner.
func (dc *DiffCollector) WriteChangeSets() error {
	return dc.inner.WriteChangeSets()
}

// WriteHistory delegates to inner.
func (dc *DiffCollector) WriteHistory() error {
	return dc.inner.WriteHistory()
}

// Accounts returns the collected account modifications.
func (dc *DiffCollector) Accounts() map[types.Address]*account.StateAccount {
	return dc.accounts
}

// AccountDeletions returns the collected account deletions.
func (dc *DiffCollector) AccountDeletions() map[types.Address]struct{} {
	return dc.accountDels
}

// Storage returns the collected storage modifications.
func (dc *DiffCollector) Storage() map[types.Address]map[types.Hash][]byte {
	return dc.storage
}

// Compile-time check.
var _ state.WriterWithChangeSets = (*DiffCollector)(nil)
