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

// Package state provides interfaces and implementations for blockchain state management.
//
// Core Interfaces:
//   - StateReader: Read-only access to account data, storage, and code
//   - StateWriter: Write access for state modifications
//   - WriterWithChangeSets: StateWriter with change tracking
//   - StateReaderWriter: Combined read/write interface
//
// Implementations:
//   - PlainStateReader: Reads from un-hashed "plain state" storage
//   - PlainStateWriter: Writes to plain state with optional change tracking
//   - HistoryStateReader: Reads historical state at specific block numbers
//   - IntraBlockState: Full state management during block execution
//
// Usage:
//   The internal/vm package should use StateReader/StateWriter interfaces
//   (via evmtypes.IntraBlockState) rather than concrete implementations,
//   enabling testability and flexibility.
package state

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// StateReader provides read-only access to blockchain state.
// This interface is used by the EVM and other components that need
// to query account data, storage, and code without modifying state.
//
// Thread Safety: Implementations must be safe for concurrent reads.
// Error Handling: nil return with nil error means the data doesn't exist.
type StateReader interface {
	// ReadAccountData returns the account state for the given address.
	// Returns nil, nil if the account doesn't exist.
	ReadAccountData(address types.Address) (*account.StateAccount, error)

	// ReadAccountStorage reads a storage slot from an account.
	// Returns nil, nil if the storage slot is empty or account doesn't exist.
	ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error)

	// ReadAccountCode returns the contract code for an account.
	// Returns nil, nil if the account has no code (EOA or empty codeHash).
	ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error)

	// ReadAccountCodeSize returns the size of the contract code.
	// Returns 0, nil if the account has no code.
	ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error)

	// ReadAccountIncarnation returns the incarnation number for an account.
	// Incarnation is incremented when a contract is destroyed and recreated.
	ReadAccountIncarnation(address types.Address) (uint16, error)
}

// StateWriter provides write access to blockchain state.
// This interface is used during block execution to modify state.
//
// Thread Safety: Implementations are NOT required to be thread-safe.
// Callers must ensure proper synchronization.
type StateWriter interface {
	// UpdateAccountData updates the account state.
	// original is the previous state (may be nil for new accounts).
	UpdateAccountData(address types.Address, original, account *account.StateAccount) error

	// UpdateAccountCode stores contract code.
	UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error

	// DeleteAccount removes an account from the state.
	DeleteAccount(address types.Address, original *account.StateAccount) error

	// WriteAccountStorage writes a storage slot.
	// original and value are the old and new values respectively.
	WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error

	// CreateContract marks an address as a contract (affects incarnation handling).
	CreateContract(address types.Address) error
}

// WriterWithChangeSets extends StateWriter with change tracking.
// This is used during block execution when we need to record
// all state changes for history/pruning purposes.
type WriterWithChangeSets interface {
	StateWriter

	// WriteChangeSets persists the accumulated change sets to storage.
	WriteChangeSets() error

	// WriteHistory persists historical data (for state history queries).
	WriteHistory() error
}

// StateReaderWriter combines StateReader and StateWriter interfaces.
// Use this when both read and write access is needed.
type StateReaderWriter interface {
	StateReader
	StateWriter
}

// Compile-time interface implementation checks
var (
	_ StateReader          = (*PlainStateReader)(nil)
	_ StateReader          = (*HistoryStateReader)(nil)
	_ WriterWithChangeSets = (*PlainStateWriter)(nil)
	_ StateWriter          = (*NoopWriter)(nil)
)

